package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "example.com/image-factory/pkg/messages" // ensure message type registration when API used standalone
	"example.com/image-factory/pkg/storage"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/lytics/grid/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	etcdv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/types/known/structpb"
)

// Server implements HTTP API.

type Server struct {
	Etcd      *etcdv3.Client
	Namespace string
	GridSrv   *grid.Server
	Store     *storage.SpannerStore // optional

	imgsDir string

	mu       sync.RWMutex
	variants map[string]map[string]string // image_id -> op -> path

	totalUploads   int
	totalVariants  int
	failedVariants int

	activeWorkers      int
	startedWorkers     int
	activeWorkersPerOp map[string]int
	successPerOp       map[string]int
	failedPerOp        map[string]int

	// SSE subscribers
	eventsMu  sync.Mutex
	eventSubs map[chan []byte]struct{}
}

func New(etcd *etcdv3.Client, ns string, gs *grid.Server, dir string, st *storage.SpannerStore) *Server {
	s := &Server{
		Etcd:               etcd,
		Namespace:          ns,
		GridSrv:            gs,
		Store:              st,
		imgsDir:            dir,
		variants:           make(map[string]map[string]string),
		activeWorkersPerOp: make(map[string]int),
		successPerOp:       make(map[string]int),
		failedPerOp:        make(map[string]int),
		eventSubs:          make(map[chan []byte]struct{}),
	}
	go s.subscribeUpdates()
	go s.subscribeSystemEvents()
	return s
}

func (s *Server) Listen(addr string) {
	r := mux.NewRouter()
	r.HandleFunc("/upload", s.handleUpload).Methods("POST")
	r.HandleFunc("/images", s.handleImages).Methods("GET")
	// Serve from Spanner if available, fallback to disk via PathPrefix
	r.HandleFunc("/images/{id}/{op}", s.handleServeVariant).Methods("GET")
	r.PathPrefix("/images/").Handler(http.StripPrefix("/images/", http.FileServer(http.Dir(s.imgsDir))))
	r.Handle("/metrics", promhttp.Handler())
	r.HandleFunc("/metrics/ui", s.handleMetricsUI).Methods("GET")
	r.HandleFunc("/metrics/json", s.handleMetricsJSON).Methods("GET")
	// Alias to avoid proxy issues
	r.HandleFunc("/stats", s.handleMetricsJSON).Methods("GET")
	// SSE stream
	r.HandleFunc("/events", s.handleEvents)
	// Admin scale
	r.HandleFunc("/admin/scale", s.handleScale).Methods("POST")

	log.Printf("HTTP API listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("api listen: %v", err)
	}
}

// --- handlers ---

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	id := uuid.New().String()
	dir := filepath.Join(s.imgsDir, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, "cannot create dir", 500)
		return
	}

	// save original
	originalExt := filepath.Ext(header.Filename)
	originalPath := filepath.Join(dir, "original"+originalExt)
	out, err := os.Create(originalPath)
	if err != nil {
		http.Error(w, "save failed", 500)
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		http.Error(w, "copy failed", 500)
		return
	}
	out.Close()

	// Save original to Spanner if configured
	if s.Store != nil {
		data, rerr := os.ReadFile(originalPath)
		if rerr != nil {
			log.Printf("spanner read original: %v", rerr)
		} else if err := s.Store.SaveOriginal(r.Context(), id, originalExt, data); err != nil {
			log.Printf("spanner save original: %v", err)
		}
	}

	// send upload event to coordinator via mailbox
	payload, _ := structpb.NewStruct(map[string]any{
		"image_id": id,
		"path":     originalPath,
	})

	client, err := grid.NewClient(s.Etcd, grid.ClientCfg{Namespace: s.Namespace})
	if err != nil {
		log.Printf("api grid client: %v", err)
		http.Error(w, "internal", 500)
		return
	}
	defer client.Close()

	if _, err := client.RequestC(r.Context(), "uploads", payload); err != nil {
		log.Printf("api upload request: %v", err)
	}

	s.totalUploads++
	s.broadcastSnapshot()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"image_id": id})
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	json.NewEncoder(w).Encode(s.variants)
}

func (s *Server) handleServeVariant(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	op := vars["op"]
	if s.Store != nil {
		data, ct, err := s.Store.GetVariant(r.Context(), id, op)
		if err == nil {
			if ct == "" {
				ct = "image/jpeg"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}
	// fallback to file path
	http.ServeFile(w, r, filepath.Join(s.imgsDir, id, op))
}

// --- subscription to transform results ---

func (s *Server) subscribeUpdates() {
	// wait for grid server running
	if err := s.GridSrv.WaitUntilStarted(context.Background()); err != nil {
		log.Printf("api updates wait: %v", err)
		return
	}
	mb, err := s.GridSrv.NewMailbox("transform-updates", 200)
	if err != nil {
		log.Printf("api updates mailbox: %v", err)
		return
	}
	defer mb.Close()

	for {
		select {
		case <-s.GridSrv.Context().Done():
			return
		case req := <-mb.C():
			msg, ok := req.Msg().(*structpb.Struct)
			if !ok {
				_ = req.Ack()
				continue
			}
			id := msg.GetFields()["image_id"].GetStringValue()
			op := msg.GetFields()["op"].GetStringValue()
			path := msg.GetFields()["path"].GetStringValue()
			s.mu.Lock()
			if _, ok := s.variants[id]; !ok {
				s.variants[id] = make(map[string]string)
			}
			s.variants[id][op] = fmt.Sprintf("/images/%s/%s", id, filepath.Base(path))
			s.mu.Unlock()

			// Save variant to Spanner if configured
			if s.Store != nil {
				data, rerr := os.ReadFile(path)
				if rerr != nil {
					log.Printf("spanner read variant: %v", rerr)
				} else if err := s.Store.SaveVariant(context.Background(), id, op+filepath.Ext(path), "image/jpeg", data); err != nil {
					log.Printf("spanner save variant: %v", err)
				}
			}

			if msg.GetFields()["success"].GetBoolValue() {
				s.totalVariants++
				s.successPerOp[op]++
			} else {
				s.failedVariants++
				s.failedPerOp[op]++
			}

			s.broadcastSnapshot()
			_ = req.Ack()
		}
	}
}

func (s *Server) subscribeSystemEvents() {
	if err := s.GridSrv.WaitUntilStarted(context.Background()); err != nil {
		log.Printf("api system-events wait: %v", err)
		return
	}
	mb, err := s.GridSrv.NewMailbox("system-events", 100)
	if err != nil {
		log.Printf("api system-events mailbox: %v", err)
		return
	}
	defer mb.Close()
	for {
		select {
		case <-s.GridSrv.Context().Done():
			return
		case req := <-mb.C():
			msg, ok := req.Msg().(*structpb.Struct)
			if !ok {
				_ = req.Ack()
				continue
			}
			evt := msg.GetFields()["event"].GetStringValue()
			op := msg.GetFields()["op"].GetStringValue()
			s.mu.Lock()
			switch evt {
			case "worker_start":
				s.startedWorkers++
				s.activeWorkers++
				s.activeWorkersPerOp[op]++
			case "worker_stop":
				if s.activeWorkers > 0 {
					s.activeWorkers--
				}
				if s.activeWorkersPerOp[op] > 0 {
					s.activeWorkersPerOp[op]--
				}
			}
			s.mu.Unlock()
			s.broadcastSnapshot()
			_ = req.Ack()
		}
	}
}

// SSE handlers and helpers
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	ch := make(chan []byte, 16)
	s.eventsMu.Lock()
	s.eventSubs[ch] = struct{}{}
	s.eventsMu.Unlock()
	defer func() {
		s.eventsMu.Lock()
		delete(s.eventSubs, ch)
		s.eventsMu.Unlock()
		close(ch)
	}()
	// send initial snapshot
	if b, err := s.snapshotJSON(); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (s *Server) snapshotJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	payload := map[string]interface{}{
		"variants": s.variants,
		"metrics": map[string]interface{}{
			"total_uploads":   s.totalUploads,
			"total_variants":  s.totalVariants,
			"failed_variants": s.failedVariants,
			"worker_active":   s.activeWorkers,
			"worker_started":  s.startedWorkers,
			"per_op": map[string]interface{}{
				"active":  s.activeWorkersPerOp,
				"success": s.successPerOp,
				"failed":  s.failedPerOp,
			},
		},
	}
	return json.Marshal(payload)
}

func (s *Server) broadcastSnapshot() {
	b, err := s.snapshotJSON()
	if err != nil {
		return
	}
	s.eventsMu.Lock()
	for ch := range s.eventSubs {
		select {
		case ch <- b:
		default:
		}
	}
	s.eventsMu.Unlock()
}

// Admin scale: POST {op:"thumbnail", n:2}
func (s *Server) handleScale(w http.ResponseWriter, r *http.Request) {
	type reqBody struct {
		Op string `json:"op"`
		N  int    `json:"n"`
	}
	var body reqBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if body.N <= 0 || body.Op == "" {
		http.Error(w, "invalid params", 400)
		return
	}
	actorType := map[string]string{
		"thumbnail": "worker-thumb",
		"grayscale": "worker-gray",
		"blur":      "worker-blur",
		"rotate90":  "worker-rot",
	}[body.Op]
	if actorType == "" {
		http.Error(w, "unknown op", 400)
		return
	}
	client, err := grid.NewClient(s.Etcd, grid.ClientCfg{Namespace: s.Namespace})
	if err != nil {
		http.Error(w, "grid client", 500)
		return
	}
	defer client.Close()
	if err := client.WaitUntilServing(r.Context(), s.GridSrv.Name()); err != nil {
		http.Error(w, "peer not serving", 500)
		return
	}
	started := 0
	for i := 0; i < body.N; i++ {
		name := fmt.Sprintf("%s-%d", actorType, time.Now().UnixNano()+int64(i))
		start := grid.NewActorStart(name)
		start.Type = actorType
		if _, err := client.RequestC(context.Background(), s.GridSrv.Name(), start); err == nil {
			started++
		}
	}
	json.NewEncoder(w).Encode(map[string]int{"started": started})
}

func (s *Server) handleMetricsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Image Factory Metrics</title>
<style>body{font-family:system-ui,Segoe UI,Roboto,Arial;margin:20px} .card{border:1px solid #ddd;border-radius:8px;padding:16px;max-width:520px}</style>
</head><body>
<h2>Metrics</h2>
<div class="card">
  <div>Total uploads: %d</div>
  <div>Total variants success: %d</div>
  <div>Total variants failed: %d</div>
  <div>Active workers: %d</div>
  <div>Workers started (lifetime): %d</div>
</div>
<p><a href="/metrics" target="_blank">Prometheus metrics</a></p>
</body></html>`, s.totalUploads, s.totalVariants, s.failedVariants, s.activeWorkers, s.startedWorkers)
}

func (s *Server) handleMetricsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{
		"total_uploads":   s.totalUploads,
		"total_variants":  s.totalVariants,
		"failed_variants": s.failedVariants,
		"worker_active":   s.activeWorkers,
		"worker_started":  s.startedWorkers,
	})
}
