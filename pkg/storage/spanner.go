package storage

import (
	"context"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
)

// SpannerStore persists images and variants into Cloud Spanner.
// Schema expected:
// CREATE TABLE Images (
//   ImageID STRING(MAX) NOT NULL,
//   Original BYTES(MAX),
//   OriginalExt STRING(16),
//   CreatedAt TIMESTAMP OPTIONS (allow_commit_timestamp=true)
// ) PRIMARY KEY (ImageID);
//
// CREATE TABLE Variants (
//   ImageID STRING(MAX) NOT NULL,
//   Op STRING(MAX) NOT NULL,
//   Data BYTES(MAX),
//   ContentType STRING(64),
//   CreatedAt TIMESTAMP OPTIONS (allow_commit_timestamp=true)
// ) PRIMARY KEY (ImageID, Op);

type SpannerStore struct {
	client *spanner.Client
	dbName string
}

func NewSpannerStore(ctx context.Context, dsn string) (*SpannerStore, error) {
	cli, err := spanner.NewClient(ctx, dsn)
	if err != nil {
		return nil, err
	}
	return &SpannerStore{client: cli, dbName: dsn}, nil
}

func (s *SpannerStore) Close() { s.client.Close() }

func (s *SpannerStore) SaveOriginal(ctx context.Context, imageID, ext string, data []byte) error {
	m := spanner.InsertOrUpdate("Images",
		[]string{"ImageID", "Original", "OriginalExt", "CreatedAt"},
		[]interface{}{imageID, data, ext, spanner.CommitTimestamp},
	)
	_, err := s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

func (s *SpannerStore) SaveVariant(ctx context.Context, imageID, op, contentType string, data []byte) error {
	m := spanner.InsertOrUpdate("Variants",
		[]string{"ImageID", "Op", "Data", "ContentType", "CreatedAt"},
		[]interface{}{imageID, op, data, contentType, spanner.CommitTimestamp},
	)
	_, err := s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

func (s *SpannerStore) GetVariant(ctx context.Context, imageID, op string) ([]byte, string, error) {
	stmt := spanner.Statement{
		SQL:    "SELECT Data, ContentType FROM Variants WHERE ImageID=@id AND Op=@op",
		Params: map[string]interface{}{"id": imageID, "op": op},
	}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return nil, "", err
	}
	var data []byte
	var ct string
	if err := row.Columns(&data, &ct); err != nil {
		return nil, "", err
	}
	return data, ct, nil
}

func (s *SpannerStore) ListOps(ctx context.Context, imageID string) ([]string, error) {
	stmt := spanner.Statement{
		SQL:    "SELECT Op FROM Variants WHERE ImageID=@id ORDER BY Op",
		Params: map[string]interface{}{"id": imageID},
	}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	ops := []string{}
	for {
		row, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var op string
		if err := row.Columns(&op); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// HealthCheck quickly pings the DB.
func (s *SpannerStore) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stmt := spanner.Statement{SQL: "SELECT 1"}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	_, err := iter.Next()
	if err == iterator.Done {
		return nil
	}
	return err
}
