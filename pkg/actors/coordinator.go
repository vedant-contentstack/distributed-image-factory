package actors

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/lytics/grid/v3"
	etcdv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	uploadsMailbox = "uploads"
)

// Coordinator receives image upload events and fans out transform tasks to workers.
// Stub implementation for initial skeleton.
type Coordinator struct {
	Server    *grid.Server
	Etcd      *etcdv3.Client
	Namespace string
}

func (c *Coordinator) Act(ctx context.Context) {
	name, _ := grid.ContextActorName(ctx)
	log.Printf("[coordinator %s] starting", name)

	mb, err := c.Server.NewMailbox(uploadsMailbox, 100)
	if err != nil {
		log.Printf("coordinator: cannot create mailbox: %v", err)
		return
	}
	defer mb.Close()

	for {
		select {
		case <-ctx.Done():
			log.Printf("coordinator exiting")
			return
		case req := <-mb.C():
			msg, ok := req.Msg().(*structpb.Struct)
			if !ok {
				_ = req.Ack()
				continue
			}
			imageID := msg.GetFields()["image_id"].GetStringValue()
			log.Printf("coordinator received upload for image %s", imageID)

			// Acknowledge to unblock sender (HTTP API)
			_ = req.Ack()

			ops := []string{"thumbnail", "grayscale", "blur", "rotate90"}

			client, err := grid.NewClient(c.Etcd, grid.ClientCfg{Namespace: c.Namespace})
			if err != nil {
				log.Printf("coordinator grid client error: %v", err)
				continue
			}

			for _, op := range ops {
				task, _ := structpb.NewStruct(map[string]any{
					"image_id": imageID,
					"op":       op,
					"path":     msg.GetFields()["path"].GetStringValue(),
				})
				ctxb, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				// Discover worker mailboxes for this op from etcd
				prefix := fmt.Sprintf("/%s/workers/%s/", c.Namespace, op)
				resp, err := c.Etcd.Get(ctxb, prefix, etcdv3.WithPrefix())
				members := []string{}
				if err == nil {
					for _, kv := range resp.Kvs {
						mbox := string(kv.Key)
						// Extract mailbox name from key suffix
						if idx := len(prefix); idx <= len(mbox) {
							members = append(members, mbox[idx:])
						}
					}
				}
				if len(members) == 0 {
					cancel()
					continue
				}
				grp := grid.NewListGroup(members...)
				_, _ = client.BroadcastC(ctxb, grp.Fastest(), task)
				cancel()
			}

			client.Close()
		}
	}
}
