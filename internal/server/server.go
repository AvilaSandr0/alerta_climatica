package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"alerta_climatica/internal/processing"
)

// Server HTTP: sirve UI y API.
type Server struct {
	state *State
	proc  *processing.Processor
	mux   *http.ServeMux
	// simple in-memory cache for proxied routes (key = "from|to")
	cache   map[string][]byte
	cacheMu sync.Mutex
}

func NewServer(state *State, proc *processing.Processor) *Server {
	s := &Server{state: state, proc: proc, mux: http.NewServeMux(), cache: make(map[string][]byte)}
	s.routes()
	// Semilla de demo para que la UI no esté vacía al iniciar.
	s.state.Seed(time.Now())
	return s
}

func (s *Server) Router() http.Handler { return s.mux }

func (s *Server) routes() {
	// UI
	s.mux.HandleFunc("/", s.handleIndex)
	// Archivos estáticos
	staticDir := http.StripPrefix("/static/", http.FileServer(http.Dir("web/static")))
	s.mux.Handle("/static/", staticDir)

	// API
	s.mux.HandleFunc("/api/sms", s.handleSMS)
	s.mux.HandleFunc("/api/alerts", s.handleAlerts)
	s.mux.HandleFunc("/api/zones", s.handleZones)
	s.mux.HandleFunc("/api/zones_geojson", s.handleZonesGeoJSON)
	s.mux.HandleFunc("/api/route", s.handleRoute)
	s.mux.HandleFunc("/api/admin/import_zones", s.handleImportZones)
	s.mux.HandleFunc("/api/reset", s.handleReset)
}

// GET /api/route?from=lon,lat&to=lon,lat
// Proxies request to OSRM public service and caches the response in-memory.
func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := q.Get("from")
	to := q.Get("to")
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		http.Error(w, "missing from/to parameters", http.StatusBadRequest)
		return
	}

	// basic validation: ensure they look like lon,lat
	if _, err := url.Parse("//" + from); err != nil {
		// not strict, we'll still try
	}

	key := from + "|" + to

	// 1) check in-memory cache
	s.cacheMu.Lock()
	if b, ok := s.cache[key]; ok {
		s.cacheMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}
	s.cacheMu.Unlock()

	// 2) check persisted cache in store (if available)
	const routeTTL = 24 * time.Hour
	if respBytes, createdAt, found, err := s.state.GetRoute(key); err == nil && found {
		if time.Since(createdAt) <= routeTTL {
			// cache in memory for faster subsequent responses
			s.cacheMu.Lock()
			s.cache[key] = respBytes
			s.cacheMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(respBytes)
			return
		}
		// expired: continue to re-fetch and replace
	}

	// 3) fetch from OSRM public API
	osrmURL := "https://router.project-osrm.org/route/v1/driving/" + url.PathEscape(from) + ";" + url.PathEscape(to) + "?overview=full&geometries=geojson"
	resp, err := http.Get(osrmURL)
	if err != nil {
		log.Println("error calling osrm:", err)
		http.Error(w, "error contacting routing service", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("error reading osrm response:", err)
		http.Error(w, "error reading routing response", http.StatusInternalServerError)
		return
	}

	// persist to DB (best-effort) and in-memory cache
	if err := s.state.SaveRoute(key, body); err != nil {
		log.Println("warning: could not persist route:", err)
	}
	s.cacheMu.Lock()
	s.cache[key] = body
	s.cacheMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// GET /api/zones_geojson: devuelve el GeoJSON de zonas enriquecido con el estado actual.
func (s *Server) handleZonesGeoJSON(w http.ResponseWriter, r *http.Request) {
	log.Println("handleZonesGeoJSON called for", r.RemoteAddr, r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	// Preferir zonas almacenadas en DB si existen
	if zlist, err := s.state.ListStoredZones(); err == nil && len(zlist) > 0 {
		log.Printf("serving %d zones from DB\n", len(zlist))
		// Construir FeatureCollection dinámicamente
		fc := map[string]interface{}{"type": "FeatureCollection", "features": []interface{}{}}
		features := make([]interface{}, 0, len(zlist))
		statuses := s.state.Zones()
		for _, z := range zlist {
			var geom interface{}
			if err := json.Unmarshal([]byte(z.Geom), &geom); err != nil {
				geom = nil
			}
			props := map[string]interface{}{"name": z.Name, "status": statuses[z.Name]}
			feat := map[string]interface{}{"type": "Feature", "properties": props, "geometry": geom}
			features = append(features, feat)
		}
		fc["features"] = features
		enc := json.NewEncoder(w)
		if err := enc.Encode(fc); err != nil {
			log.Println("error encoding geojson response:", err)
		}
		return
	}

	// Fallback: leer archivo GeoJSON desde web/static
	path := filepath.Join("web", "static", "zones.geojson")
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "no se pudo leer zones.geojson", http.StatusInternalServerError)
		log.Println("error reading zones.geojson:", err)
		return
	}

	var fc map[string]interface{}
	if err := json.Unmarshal(data, &fc); err != nil {
		http.Error(w, "geojson inválido", http.StatusInternalServerError)
		log.Println("error unmarshalling zones.geojson:", err)
		return
	}

	// Obtener estado actual de zonas
	statuses := s.state.Zones()

	// Enriquecer cada feature con properties.status si existe name
	if features, ok := fc["features"].([]interface{}); ok {
		for _, f := range features {
			if fm, ok := f.(map[string]interface{}); ok {
				if props, ok := fm["properties"].(map[string]interface{}); ok {
					if nameI, ok := props["name"]; ok {
						if name, ok := nameI.(string); ok {
							if st, found := statuses[name]; found {
								props["status"] = st
							} else {
								props["status"] = "verde"
							}
						}
					}
				}
			}
		}
	}

	enc := json.NewEncoder(w)
	if err := enc.Encode(fc); err != nil {
		log.Println("error encoding geojson response:", err)
	}
}

// POST /api/admin/import_zones: importa un FeatureCollection GeoJSON al almacenamiento.
// Para demo no tiene autenticación; en producción proteger este endpoint.
func (s *Server) handleImportZones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "error leyendo body", http.StatusBadRequest)
		return
	}
	if err := s.state.ImportZones(data); err != nil {
		log.Println("import zones failed:", err)
		http.Error(w, "import failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join("web", "index.html"))
}

// POST /api/sms: recibe JSON o application/x-www-form-urlencoded con campos
// "texto" y "zona". Encola el mensaje para procesamiento concurrente.
func (s *Server) handleSMS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var in processing.IncomingMessage

	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "JSON inválido", http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Formulario inválido", http.StatusBadRequest)
			return
		}
		in.Zone = r.FormValue("zona")
		in.Text = r.FormValue("texto")
	}
	if strings.TrimSpace(in.Zone) == "" {
		in.Zone = "Zona Centro" // por defecto
	}
	in.ReceivedAt = time.Now()

	s.proc.Submit(in)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "enviado"})
}

// GET /api/alerts: devuelve alertas recientes.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	alerts := s.state.ListAlerts()
	if err := json.NewEncoder(w).Encode(alerts); err != nil {
		log.Println("error serializando alerts:", err)
	}
}

// GET /api/zones: mapa de colores por zona.
func (s *Server) handleZones(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	z := s.state.Zones()
	if err := json.NewEncoder(w).Encode(z); err != nil {
		log.Println("error serializando zones:", err)
	}
}

// POST /api/reset: retorna zonas a verde (demo)
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.state.ResetZones()
	w.WriteHeader(http.StatusNoContent)
}
