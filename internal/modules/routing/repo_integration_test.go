package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in, read-only integration tests against the real seven-floor Mingde fixture.
// ROUTING_TEST_DATABASE_URL=postgres://... go test ./internal/modules/routing -run Integration -v
func TestMingdeRouteIntegration(t *testing.T) {
	dsn := os.Getenv("ROUTING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ROUTING_TEST_DATABASE_URL to test the real Mingde routing graph")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var readOnly string
	if err := pool.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		t.Fatalf("read-only connection required: %q, %v", readOnly, err)
	}
	repo := NewRepo(pool)
	dest, err := repo.NodeForFloor(ctx, "OSM-Way862952692", 6)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		from, to int64
		descend  bool
	}{
		{"up", 4273, dest, false},
		{"down", dest, 4273, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := repo.Route(ctx, tc.from, tc.to, false)
			if err != nil || len(nodes) < 2 {
				t.Fatalf("route: %v, %d nodes", err, len(nodes))
			}
			if nodes[0].NodeID != tc.from || nodes[len(nodes)-1].NodeID != tc.to {
				t.Fatal("incorrect route endpoints")
			}
			last := nodes[len(nodes)-1]
			if last.EdgeID != -1 || last.Cost != 0 || len(last.EdgeGeom) != 0 || last.FloorChange {
				t.Fatalf("terminal row must have no outgoing edge: %+v", last)
			}
			var cost float64
			reversed := 0
			var expectedCoords [][]float64
			appendDistinct := func(dst *[][]float64, points [][]float64) {
				for _, p := range points {
					if len(*dst) == 0 || !samePoint((*dst)[len(*dst)-1], p) {
						*dst = append(*dst, p)
					}
				}
			}
			for i, n := range nodes[:len(nodes)-1] {
				next := nodes[i+1]
				var source, target int64
				var forwardCost, reverseCost float64
				if err := pool.QueryRow(ctx, `SELECT source, target, cost_m, reverse_cost_m FROM nav_edges WHERE edge_id=$1`, n.EdgeID).Scan(&source, &target, &forwardCost, &reverseCost); err != nil {
					t.Fatal(err)
				}
				wantCost := forwardCost
				switch {
				case source == n.NodeID && target == next.NodeID:
				case target == n.NodeID && source == next.NodeID:
					reversed++
					wantCost = reverseCost
				default:
					t.Fatalf("edge %d does not join outgoing node pair %d -> %d", n.EdgeID, n.NodeID, next.NodeID)
				}
				if math.Abs(n.Cost-wantCost) > 1e-8 {
					t.Fatalf("edge %d cost %f, want %f", n.EdgeID, n.Cost, wantCost)
				}
				cost += wantCost
				points, ok := lineCoords(n.EdgeGeom)
				if !ok || !samePoint(points[0], []float64{n.Lng, n.Lat}) || !samePoint(points[len(points)-1], []float64{next.Lng, next.Lat}) {
					t.Fatalf("edge %d geometry must follow %d -> %d: %s", n.EdgeID, n.NodeID, next.NodeID, n.EdgeGeom)
				}
				appendDistinct(&expectedCoords, points)
			}
			if reversed == 0 {
				t.Fatal("fixture must exercise reverse traversal")
			}

			// Exercise the actual HTTP handler in memory, without restarting :8080.
			router := gin.New()
			Register(router.Group("/api/v1"), nil, pool)
			body := fmt.Sprintf(`{"origin":{"node_id":%d},"destination":{"node_id":%d}}`, tc.from, tc.to)
			if !tc.descend {
				body = `{"origin":{"node_id":4273},"destination":{"building_id":"OSM-Way862952692","level_index":6}}`
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/route", strings.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", resp.Code, resp.Body.String())
			}
			var envelope struct {
				Data RouteResult `json:"data"`
			}
			if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			res := envelope.Data
			if res.OriginNodeID != tc.from || res.DestNodeID != tc.to || res.TotalLengthM != math.Round(cost*10)/10 {
				t.Fatalf("endpoints/total incorrect: %d -> %d, total %.1f, raw cost %.4f", res.OriginNodeID, res.DestNodeID, res.TotalLengthM, cost)
			}
			changes := 0
			var actualCoords [][]float64
			for _, s := range res.Segments {
				points, ok := lineCoords(s.Geometry)
				if !ok {
					t.Fatalf("invalid geometry: %s", s.Geometry)
				}
				appendDistinct(&actualCoords, points)
				if s.Type != "floor_change" {
					continue
				}
				from, to := changes+1, changes+2
				if tc.descend {
					from, to = 7-changes, 6-changes
				}
				if s.FromFloorName != fmt.Sprintf("%dF", from) || s.ToFloorName != fmt.Sprintf("%dF", to) {
					t.Fatalf("floor change %d: %+v", changes, s)
				}
				changes++
			}
			if changes != 6 {
				t.Fatalf("got %d floor changes, want 6: %v", changes, res.Steps)
			}
			if len(actualCoords) != len(expectedCoords) {
				t.Fatalf("route geometry duplicates/skips edge coordinates: got %d, want %d", len(actualCoords), len(expectedCoords))
			}
			for i := range actualCoords {
				if !samePoint(actualCoords[i], expectedCoords[i]) {
					t.Fatalf("geometry backtracks at %d: got %v, want %v", i, actualCoords[i], expectedCoords[i])
				}
			}
			t.Logf("%d -> %d: %d nodes, %d reversed edges, raw cost %.4fm, response %.1fm; steps=%v", tc.from, tc.to, len(nodes), reversed, cost, res.TotalLengthM, res.Steps)
		})
	}
}

func samePoint(a, b []float64) bool {
	// Allow centimetre-scale GeoJSON rounding / nonzero stair geometry offsets.
	return len(a) >= 2 && len(b) >= 2 && math.Abs(a[0]-b[0]) < 1e-7 && math.Abs(a[1]-b[1]) < 1e-7
}
