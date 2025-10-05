package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Tenant struct {
	ID string `json:"id"`
	Name string `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Shift struct {
	ID string `json:"id"`
	TenantID string `json:"tenant_id"`
	Title string `json:"title"`
	StartedAt *time.Time `json:"starts_at,omitempty"`
	EndsAt *time.Time `json:"ends_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}


// json helpers
func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonCreated(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}


type App struct {
	DB *pgxpool.Pool
}

// -------- Handlers -------------
func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

// GET /api/tenants
func (a *App) listTenants(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.Query(r.Context(),`
		SELECT id::text, name, created_at
		FROM tenants
		ORDER BY created_at DESC`)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOK(w, out)
}

// post /api/tenants { "name": "Acme Security"}
func (a *App) createTenant(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name string `json:"name"`}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "invalid json")
		return
	}
	if len(body.Name) < 2 {
		jsonError(w, 422, "name must be at least 2 characters")
		return
	}

	var t Tenant
	err := a.DB.QueryRow(r.Context(), `
		INSERT INTO tenants (name)
		VALUES ($1)
		RETURNING id::text, name, created_at`,
		body.Name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		jsonError(w, 500, err.Error())
	}
	jsonCreated(w, t)
}

// GET /api/shifts?tenant_id=UUID
func (a *App) listShifts(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")

	query := `
		SELECT id::text, tenant_id::text, title, starts_at, ends_at, created_at
		FROM shifts`
	args := []any{}
	if tenantID != "" {
		query += " WHERE tenant_id = $1"
		args = append(args, tenantID)
	}
	query += " ORDER BY created_at DESC"

	rows, err := a.DB.Query(r.Context(), query, args...)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	defer rows.Close()

	var out []Shift
	for rows.Next() {
		var s Shift
		if err := rows.Scan(&s.ID, &s.TenantID, &s.Title, &s.StartsAt, &s.EndsAt, &s.CreatedAt); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOK(w, out)
}

// POST /api/shifts
func (a *App) createShift(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantID string `json:"tenant_id"`
		Title string `json:"title"`
		StartsAt *string `json:"starts_at"`
		EndsAt *string `json:"ends_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "invalid json")
		return
	}
	if body.TenantID == "" || body.Title == "" {
		jsonError(w, 422, "tenant_id and title required")
		return
	}

	var st, et *time.Time
	if body.StartsAt != nil && *body.StartsAt != "" {
		t, err := time.Parse(time.RFC3339, *body.StartsAt)
		if err != nil {
			jsonError(w, 422, "invalid starts_at format (RFC3339)")
			return
		}
		st = &t
	}
	if body.EndsAt != nil && *body.EndsAt != "" {
		t, err := time.Parse(time.RFC3339, *body.EndsAt)
		if err != nil {
			jsonError(w, 422, "invalid ends_at format (RFC3339)")
			return
		}
		et = &t
	}

	var s Shift
	err := a.DB.QueryRow(r.Context(), `
		INSERT INTO shifts (tenant_id, title, starts_at, ends_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, tenant_id::text, title, starts_at, ends_at, created_at`,
		body.TenantID, body.Title, st, et,
	).Scan(&s.ID, &s.TenantID, &s.Title, &s.StartsAt, &s.EndsAt, &s.CreatedAt)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonCreated(w, s)	
}

// main function

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal(err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	app := &App{DB: pool}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.healthz)
	mux.Handle("GET /api/tenants", withCORS(http.HandlerFunc(app.listTenants)))
	mux.Handle("POST /api/tenants", withCORS(http.HandlerFunc(app.createTenant)))
	mux.Handle("GET /api/shifts", withCORS(http.HandlerFunc(app.listShifts)))
	mux.Handle("POST /api/shifts", withCORS(http.HandlerFunc(app.createShift)))

	srv := &http.Server{
		Addr: ":8080",
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Println("API listening on :8080")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}