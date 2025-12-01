package server

import (
	"log"
	"sort"
	"sync"
	"time"

	"alerta_climatica/internal/processing"
	"alerta_climatica/internal/storage"
)

// State mantiene el estado en memoria de alertas y estado por zona.
type State struct {
	mu         sync.RWMutex
	alerts     []processing.Alert
	zoneStatus map[string]string // zona -> color ("verde", "amarillo", "rojo")
	store      storage.Store
}

// NewState crea el estado y puede recibir un storage.Store (nil para solo memoria).
func NewState(store storage.Store) *State {
	s := &State{
		alerts:     make([]processing.Alert, 0, 256),
		zoneStatus: make(map[string]string),
		store:      store,
	}
	
	// Inicializar zonas desde la base de datos si están disponibles
	if store != nil {
		if zones, err := store.ListZones(); err == nil && len(zones) > 0 {
			for _, z := range zones {
				s.zoneStatus[z.Name] = "verde"
			}
		} else {
			// Fallback: zonas por defecto si no hay en la DB
			s.zoneStatus["Zona Norte"] = "verde"
			s.zoneStatus["Zona Centro"] = "verde"
			s.zoneStatus["Zona Sur"] = "verde"
		}
	} else {
		// Sin store: usar zonas por defecto
		s.zoneStatus["Zona Norte"] = "verde"
		s.zoneStatus["Zona Centro"] = "verde"
		s.zoneStatus["Zona Sur"] = "verde"
	}
	
	return s
}

// ImportZones importa un FeatureCollection GeoJSON al store si está disponible.
func (s *State) ImportZones(data []byte) error {
	if s.store == nil {
		return nil
	}
	return s.store.ImportZonesFromGeoJSON(data)
}

// ListStoredZones devuelve zonas desde el store (si existe).
func (s *State) ListStoredZones() ([]storage.Zone, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListZones()
}

// AddAlert agrega una alerta y actualiza el estado de la zona.
// Si se dispone de store, intenta persistir la alerta en segundo plano (de forma no bloqueante).
func (s *State) AddAlert(a processing.Alert) {
	// Actualizar estado en memoria rápidamente
	s.mu.Lock()
	s.alerts = append(s.alerts, a)
	if len(s.alerts) > 500 {
		s.alerts = s.alerts[len(s.alerts)-500:]
	}
	// Asegurar que la zona existe en el mapa
	if _, exists := s.zoneStatus[a.Zone]; !exists {
		s.zoneStatus[a.Zone] = "verde"
	}
	switch a.Severity {
	case "crítica":
		s.zoneStatus[a.Zone] = "rojo"
	case "alta":
		if s.zoneStatus[a.Zone] != "rojo" {
			s.zoneStatus[a.Zone] = "amarillo"
		}
	default:
	}
	s.mu.Unlock()

	// Persistir de forma síncrona para garantizar durabilidad.
	if s.store != nil {
		if err := s.store.SaveAlert(a); err != nil {
			log.Println("warning: failed to persist alert:", err)
		}
	}
}

// Close espera que las persistencias pendientes terminen y cierra el store si existe.
func (s *State) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

// ListAlerts retorna las alertas; si hay store, intenta leer desde la DB.
func (s *State) ListAlerts() []processing.Alert {
	if s.store != nil {
		if list, err := s.store.ListAlerts(); err == nil {
			return list
		} else {
			log.Println("warning: failed to read alerts from store, falling back to memory:", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]processing.Alert, len(s.alerts))
	copy(out, s.alerts)
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out
}

// Zones retorna el mapa de estado de zonas.
func (s *State) Zones() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.zoneStatus))
	for k, v := range s.zoneStatus {
		out[k] = v
	}
	return out
}

// ResetZones permite resetear niveles a verde (útil en demo).
func (s *State) ResetZones() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.zoneStatus {
		s.zoneStatus[k] = "verde"
	}
}

// Seed agrega algunas alertas de ejemplo (para la primera carga de UI).
func (s *State) Seed(now time.Time) {
	demo := processing.Alert{ID: "demo1", Zone: "Zona Norte", Type: "informativo", Severity: "baja", Message: "Inicio del sistema", Timestamp: now}
	s.AddAlert(demo)
}

// SaveRoute guarda la respuesta raw de routing en el store si está disponible.
func (s *State) SaveRoute(key string, response []byte) error {
	if s.store == nil {
		return nil
	}
	return s.store.SaveRoute(key, response)
}

// GetRoute devuelve la respuesta guardada y su timestamp (found==false si no existe).
func (s *State) GetRoute(key string) (response []byte, createdAt time.Time, found bool, err error) {
	if s.store == nil {
		return nil, time.Time{}, false, nil
	}
	return s.store.GetRoute(key)
}
