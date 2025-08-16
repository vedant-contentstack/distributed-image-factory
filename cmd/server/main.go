package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"example.com/image-factory/pkg/actors"
	"example.com/image-factory/pkg/api"
	_ "example.com/image-factory/pkg/messages" // ensure protobuf Struct is registered
	"example.com/image-factory/pkg/storage"
	"github.com/lytics/grid/v3"
	etcd "go.etcd.io/etcd/client/v3"
)

func main() {
	endpoints := []string{os.Getenv("ETCD_ENDPOINT")}
	if endpoints[0] == "" {
		endpoints = []string{"localhost:2379"}
	}

	cli, err := etcd.New(etcd.Config{Endpoints: endpoints})
	if err != nil {
		log.Fatalf("failed to connect to etcd: %v", err)
	}
	defer cli.Close()

	namespace := "imgsvc"

	server, err := grid.NewServer(cli, grid.ServerCfg{Namespace: namespace})
	if err != nil {
		log.Fatalf("grid server: %v", err)
	}

	// Register actor definitions
	server.RegisterDef("leader", func(_ []byte) (grid.Actor, error) {
		return &actors.Coordinator{Server: server, Etcd: cli, Namespace: namespace}, nil
	})
	server.RegisterDef("worker-thumb", func(_ []byte) (grid.Actor, error) {
		return &actors.Worker{Server: server, Etcd: cli, Namespace: namespace, SupportedOp: "thumbnail"}, nil
	})
	server.RegisterDef("worker-gray", func(_ []byte) (grid.Actor, error) {
		return &actors.Worker{Server: server, Etcd: cli, Namespace: namespace, SupportedOp: "grayscale"}, nil
	})
	server.RegisterDef("worker-blur", func(_ []byte) (grid.Actor, error) {
		return &actors.Worker{Server: server, Etcd: cli, Namespace: namespace, SupportedOp: "blur"}, nil
	})
	server.RegisterDef("worker-rot", func(_ []byte) (grid.Actor, error) {
		return &actors.Worker{Server: server, Etcd: cli, Namespace: namespace, SupportedOp: "rotate90"}, nil
	})

	// Listen and serve grid
	addr := os.Getenv("GRID_BIND")
	if addr == "" {
		addr = "127.0.0.1:9100"
	}
	host, port, _ := net.SplitHostPort(addr)
	if host == "" || host == "0.0.0.0" || strings.HasPrefix(host, "::") {
		if ip := firstPrivateIPv4(); ip != "" {
			addr = net.JoinHostPort(ip, port)
		}
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	go func() {
		log.Println("Grid listening on", addr)
		if err := server.Serve(lis); err != nil {
			log.Fatalf("serve error: %v", err)
		}
	}()

	if err := server.WaitUntilStarted(context.Background()); err != nil {
		log.Fatalf("grid start error: %v", err)
	}

	// Optional Spanner store
	var store *storage.SpannerStore
	if dsn := os.Getenv("SPANNER_DSN"); dsn != "" {
		st, err := storage.NewSpannerStore(context.Background(), dsn)
		if err != nil {
			log.Printf("spanner init error: %v", err)
		} else {
			store = st
			log.Printf("spanner store initialized: %s", dsn)
		}
	}

	// Start HTTP API
	imgsDir := "./data"
	_ = os.MkdirAll(imgsDir, 0755)
	go func() {
		apiSrv := api.New(cli, namespace, server, imgsDir, store)
		apiSrv.Listen(":8080")
	}()

	// Start local per-op workers with unique names
	if v := os.Getenv("AUTO_START_LOCAL_WORKERS"); v == "1" || strings.ToLower(v) == "true" {
		go startWorker(clientConfig{cli, namespace}, server.Name(), "worker-thumb")
		go startWorker(clientConfig{cli, namespace}, server.Name(), "worker-gray")
		go startWorker(clientConfig{cli, namespace}, server.Name(), "worker-blur")
		go startWorker(clientConfig{cli, namespace}, server.Name(), "worker-rot")
	}

	// Block forever
	select {}
}

type clientConfig struct {
	cli       *etcd.Client
	namespace string
}

func startWorker(cfg clientConfig, serverName, actorType string) {
	client, err := grid.NewClient(cfg.cli, grid.ClientCfg{Namespace: cfg.namespace})
	if err != nil {
		log.Printf("client: %v", err)
		return
	}
	defer client.Close()

	if err := client.WaitUntilServing(context.Background(), serverName); err != nil {
		log.Printf("peer not serving: %v", err)
		return
	}

	name := fmt.Sprintf("%s-%d", actorType, time.Now().UnixNano())
	start := grid.NewActorStart(name)
	start.Type = actorType
	if _, err := client.RequestC(context.Background(), serverName, start); err != nil {
		log.Printf("start %s error: %v", actorType, err)
	} else {
		log.Printf("%s started as %s", actorType, name)
	}
}

// firstPrivateIPv4 returns the first non-loopback IPv4 address of the host.
func firstPrivateIPv4() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifs {
		if (iface.Flags & net.FlagUp) == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			return ip.String()
		}
	}
	return ""
}
