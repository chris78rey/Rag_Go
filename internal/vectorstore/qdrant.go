package vectorstore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the Qdrant gRPC client.
type Client struct {
	conn       *grpc.ClientConn
	points     pb.PointsClient
	collections pb.CollectionsClient
	collection string
	vectorSize uint64
}

// SearchResult represents a single hit from semantic search.
type SearchResult struct {
	Text     string
	Filename string
	Section  string
	ChunkIdx int
	Score    float32
}

// NewClient connects to Qdrant and ensures the collection exists.
func NewClient(ctx context.Context, host, port, collection string, vectorSize uint64) (*Client, error) {
	addr := fmt.Sprintf("%s:%s", host, port)
	slog.Info("conectando_qdrant", "addr", addr)

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("conectando a Qdrant: %w", err)
	}

	c := &Client{
		conn:        conn,
		points:      pb.NewPointsClient(conn),
		collections: pb.NewCollectionsClient(conn),
		collection:  collection,
		vectorSize:  vectorSize,
	}

	if err := c.ensureCollection(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("asegurando colección: %w", err)
	}

	return c, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) ensureCollection(ctx context.Context) error {
	// Check if collection exists
	listResp, err := c.collections.List(ctx, &pb.ListCollectionsRequest{})
	if err != nil {
		return fmt.Errorf("listando colecciones: %w", err)
	}

	for _, col := range listResp.Collections {
		if col.Name == c.collection {
			slog.Info("coleccion_existente", "nombre", c.collection)
			return nil
		}
	}

	slog.Info("creando_coleccion", "nombre", c.collection, "vector_size", c.vectorSize)
	_, err = c.collections.Create(ctx, &pb.CreateCollection{
		CollectionName: c.collection,
		VectorsConfig: &pb.VectorsConfig{
			Config: &pb.VectorsConfig_Params{
				Params: &pb.VectorParams{
					Size:     c.vectorSize,
					Distance: pb.Distance_Cosine,
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("creando colección: %w", err)
	}

	slog.Info("coleccion_creada", "nombre", c.collection)
	return nil
}

// Upsert inserts or updates vectors with their payloads.
func (c *Client) Upsert(ctx context.Context, vectors [][]float64, payloads []map[string]interface{}) error {
	if len(vectors) != len(payloads) {
		return fmt.Errorf("vectors y payloads deben ser del mismo tamaño")
	}

	var points []*pb.PointStruct
	for i, vec := range vectors {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s|%v|%v|%v", c.collection, payloads[i]["document_id"], payloads[i]["filename"], payloads[i]["chunk_index"]))).String()

		// Convert float64 vector to float32 (Qdrant requirement)
		vec32 := make([]float32, len(vec))
		for j, v := range vec {
			vec32[j] = float32(v)
		}

		// Convert payload
		pbPayload := make(map[string]*pb.Value)
		for k, v := range payloads[i] {
			switch val := v.(type) {
			case string:
				pbPayload[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: val}}
			case int:
				pbPayload[k] = &pb.Value{Kind: &pb.Value_IntegerValue{IntegerValue: int64(val)}}
			case float64:
				pbPayload[k] = &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: val}}
			}
		}

		points = append(points, &pb.PointStruct{
			Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: id}},
			Vectors: &pb.Vectors{VectorsOptions: &pb.Vectors_Vector{Vector: &pb.Vector{Data: vec32}}},
			Payload: pbPayload,
		})
	}

	wait := true
	_, err := c.points.Upsert(ctx, &pb.UpsertPoints{
		CollectionName: c.collection,
		Points:         points,
		Wait:           &wait,
	})
	if err != nil {
		return fmt.Errorf("insertando vectores: %w", err)
	}

	slog.Info("vectores_insertados", "cantidad", len(vectors), "coleccion", c.collection)
	return nil
}

// Search performs semantic search returning the top-K results.
func (c *Client) Search(ctx context.Context, vector []float64, topK uint64) ([]SearchResult, error) {
	start := time.Now()

	// Convert vector to float32 (Qdrant expects float32)
	vec32 := make([]float32, len(vector))
	for i, v := range vector {
		vec32[i] = float32(v)
	}

	resp, err := c.points.Search(ctx, &pb.SearchPoints{
		CollectionName: c.collection,
		Vector:         vec32,
		Limit:          topK,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
	})
	if err != nil {
		return nil, fmt.Errorf("buscando en Qdrant: %w", err)
	}

	var results []SearchResult
	for _, hit := range resp.Result {
		r := parseSearchHit(hit)
		results = append(results, r)
	}

	slog.Info("busqueda_completada", "fragmentos_recuperados", len(results), "tiempo_ms", time.Since(start).Milliseconds())
	return results, nil
}

// SearchByUser performs semantic search filtered by user_id in payload.
func (c *Client) SearchByUser(ctx context.Context, vector []float64, userID string, topK uint64) ([]SearchResult, error) {
	start := time.Now()

	vec32 := make([]float32, len(vector))
	for i, v := range vector {
		vec32[i] = float32(v)
	}

	resp, err := c.points.Search(ctx, &pb.SearchPoints{
		CollectionName: c.collection,
		Vector:         vec32,
		Limit:          topK,
		WithPayload:    &pb.WithPayloadSelector{SelectorOptions: &pb.WithPayloadSelector_Enable{Enable: true}},
		Filter: &pb.Filter{
			Must: []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "user_id",
							Match: &pb.Match{
								MatchValue: &pb.Match_Keyword{Keyword: userID},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("buscando en Qdrant: %w", err)
	}

	var results []SearchResult
	for _, hit := range resp.Result {
		r := parseSearchHit(hit)
		results = append(results, r)
	}

	slog.Info("busqueda_completada", "fragmentos_recuperados", len(results), "user_id", userID, "tiempo_ms", time.Since(start).Milliseconds())
	return results, nil
}

func parseSearchHit(hit *pb.ScoredPoint) SearchResult {
	r := SearchResult{}
	if hit.Payload == nil {
		return r
	}
	if v, ok := hit.Payload["text"]; ok {
		r.Text = v.GetStringValue()
	}
	if v, ok := hit.Payload["filename"]; ok {
		r.Filename = v.GetStringValue()
	}
	if v, ok := hit.Payload["section"]; ok {
		r.Section = v.GetStringValue()
	}
	if v, ok := hit.Payload["chunk_index"]; ok {
		r.ChunkIdx = int(v.GetIntegerValue())
	}
	r.Score = hit.Score
	return r
}

// DeleteByDocumentID deletes all vectors with matching document_id in payload.
func (c *Client) DeleteByDocumentID(ctx context.Context, documentID string) error {
	_, err := c.points.Delete(ctx, &pb.DeletePoints{
		CollectionName: c.collection,
		Points: &pb.PointsSelector{
			PointsSelectorOneOf: &pb.PointsSelector_Filter{
				Filter: &pb.Filter{
					Must: []*pb.Condition{
						{
							ConditionOneOf: &pb.Condition_Field{
								Field: &pb.FieldCondition{
									Key: "document_id",
									Match: &pb.Match{
										MatchValue: &pb.Match_Keyword{Keyword: documentID},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("eliminando vectores: %w", err)
	}

	slog.Info("vectores_eliminados", "document_id", documentID, "coleccion", c.collection)
	return nil
}