package actors

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"path/filepath"

	"github.com/disintegration/imaging"
	"github.com/lytics/grid/v3"
	etcdv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/types/known/structpb"
)

// Worker performs image transformations for a specific operation.
type Worker struct {
	Server      *grid.Server
	Etcd        *etcdv3.Client
	Namespace   string
	SupportedOp string
}

func (w *Worker) Act(ctx context.Context) {
	name, _ := grid.ContextActorName(ctx)
	log.Printf("[worker %s] starting (op=%s)", name, w.SupportedOp)

	mailboxName := "worker"
	if w.SupportedOp != "" {
		mailboxName = "worker-" + w.SupportedOp + "-" + name
	}

	// Announce start
	if c, err := grid.NewClient(w.Etcd, grid.ClientCfg{Namespace: w.Namespace}); err == nil {
		if msg, _ := structpb.NewStruct(map[string]any{"event": "worker_start", "name": name, "op": w.SupportedOp, "mailbox": mailboxName}); msg != nil {
			c.RequestC(context.Background(), "system-events", msg)
		}
		c.Close()
	}
	// Register in etcd for coordinator discovery
	key := fmt.Sprintf("/%s/workers/%s/%s", w.Namespace, w.SupportedOp, mailboxName)
	_, _ = w.Etcd.Put(context.Background(), key, "")

	mb, err := w.Server.NewMailbox(mailboxName, 100)
	if err != nil {
		if errors.Is(err, grid.ErrAlreadyRegistered) {
			log.Printf("worker: mailbox %s already registered on this peer; another worker is running. exiting.", mailboxName)
			_, _ = w.Etcd.Delete(context.Background(), key)
			return
		}
		log.Printf("worker: cannot create mailbox: %v", err)
		_, _ = w.Etcd.Delete(context.Background(), key)
		return
	}
	defer mb.Close()

	defer func() {
		// Announce stop and deregister
		if c, err := grid.NewClient(w.Etcd, grid.ClientCfg{Namespace: w.Namespace}); err == nil {
			if msg, _ := structpb.NewStruct(map[string]any{"event": "worker_stop", "name": name, "op": w.SupportedOp, "mailbox": mailboxName}); msg != nil {
				c.RequestC(context.Background(), "system-events", msg)
			}
			c.Close()
		}
		_, _ = w.Etcd.Delete(context.Background(), key)
	}()

	for {
		select {
		case <-ctx.Done():
			log.Printf("worker exiting")
			return
		case req := <-mb.C():
			task, ok := req.Msg().(*structpb.Struct)
			if !ok {
				_ = req.Ack()
				continue
			}
			imageID := task.GetFields()["image_id"].GetStringValue()
			op := task.GetFields()["op"].GetStringValue()
			if w.SupportedOp != "" && op != w.SupportedOp {
				// Wrong queue; ack and ignore
				_ = req.Ack()
				continue
			}
			log.Printf("[worker %s] received task: %s %s", name, imageID, op)

			// Determine paths
			baseDir := filepath.Dir(task.GetFields()["path"].GetStringValue())
			original := task.GetFields()["path"].GetStringValue()
			variantPath := filepath.Join(baseDir, op+".jpg")

			// Perform transform
			success := true
			if err := w.doTransform(original, variantPath, op); err != nil {
				log.Printf("worker transform error: %v", err)
				success = false
			}

			result, _ := structpb.NewStruct(map[string]any{
				"image_id": imageID,
				"op":       op,
				"success":  success,
				"path":     variantPath,
			})

			// Respond to coordinator
			_ = req.Respond(result)

			// Also send to transform-updates mailbox so API can pick it up (success or failure)
			if upd, err := grid.NewClient(w.Etcd, grid.ClientCfg{Namespace: w.Namespace}); err == nil {
				upd.RequestC(context.Background(), "transform-updates", result)
				upd.Close()
			}
		}
	}
}

func (w *Worker) doTransform(src, dst, op string) error {
	img, err := imaging.Open(src)
	if err != nil {
		return err
	}
	var outImg *image.NRGBA
	switch op {
	case "thumbnail":
		outImg = imaging.Thumbnail(img, 200, 200, imaging.Lanczos)
	case "grayscale":
		outImg = imaging.Grayscale(img)
	case "blur":
		outImg = imaging.Blur(img, 3.0)
	case "rotate90":
		outImg = imaging.Rotate90(img)
	default:
		return fmt.Errorf("unknown op %s", op)
	}
	if err := imaging.Save(outImg, dst); err != nil {
		return err
	}
	return nil
}
