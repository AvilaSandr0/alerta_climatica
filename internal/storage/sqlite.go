package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"alerta_climatica/internal/processing"

	_ "modernc.org/sqlite"
)

// Store es la interfaz mínima usada por el servidor para persistir alertas.
type Store interface {
	SaveAlert(a processing.Alert) error
	ListAlerts() ([]processing.Alert, error)
	// Zones-related methods
	ImportZonesFromGeoJSON(data []byte) error
	ListZones() ([]Zone, error)
	// Routes persistence (keyed cache)
	SaveRoute(key string, response []byte) error
	GetRoute(key string) (response []byte, createdAt time.Time, found bool, err error)
	// Chat persistence
	SaveChat(role string, content string) error
	ListChats(limit int) ([]ChatMessage, error)
	Close() error
}

// Zone representa una zona geográfica almacenada (geom es GeoJSON geometry as text).
type Zone struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Geom string `json:"geom"`
}

// ImportZonesFromGeoJSON importa un FeatureCollection GeoJSON (bytes) a la tabla zones.
// Reemplaza el contenido actual de la tabla.
func (s *SQLiteStore) ImportZonesFromGeoJSON(data []byte) error {
	// parse minimal structure to extract features
	var fc map[string]interface{}
	if err := json.Unmarshal(data, &fc); err != nil {
		return err
	}

	feats, ok := fc["features"].([]interface{})
	if !ok {
		return nil // nada que hacer
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM zones`); err != nil {
		tx.Rollback()
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO zones(name, geom, created_at) VALUES(?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, f := range feats {
		fm, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		props, _ := fm["properties"].(map[string]interface{})
		var name string
		if n, ok := props["name"].(string); ok {
			name = n
		} else if n, ok := props["nombre"].(string); ok {
			name = n
		} else {
			// fallback to id
			if idv, ok := fm["id"]; ok {
				name = fmt.Sprint(idv)
			}
		}
		geomObj := fm["geometry"]
		geomBytes, err := json.Marshal(geomObj)
		if err != nil {
			geomBytes = []byte("null")
		}

		if _, err := stmt.Exec(name, string(geomBytes), time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// ListZones devuelve las zonas almacenadas en la DB.
func (s *SQLiteStore) ListZones() ([]Zone, error) {
	rows, err := s.db.Query(`SELECT id, name, geom FROM zones ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Zone, 0)
	for rows.Next() {
		var z Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.Geom); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, nil
}

// SQLiteStore implementa Store usando sqlite (pure Go driver modernc.org/sqlite).
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLite abre (o crea) la base de datos en path y aplica esquema mínimo.
func NewSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Activar WAL para mejor concurrencia en escrituras pequeñas.
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		log.Println("warning: could not set WAL mode:", err)
	}

	schema := `CREATE TABLE IF NOT EXISTS alerts (
        id TEXT PRIMARY KEY,
        zone TEXT,
        type TEXT,
        severity TEXT,
        message TEXT,
        extract TEXT,
        timestamp TEXT
    );
    CREATE TABLE IF NOT EXISTS zones (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL,
        geom TEXT,
        created_at TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_alerts_timestamp ON alerts(timestamp);`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Crear tabla zones si no existe (almacena geom como GeoJSON text)
	zonesSchema := `CREATE TABLE IF NOT EXISTS zones (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		geom TEXT,
		created_at TEXT
	);`

	if _, err := db.Exec(zonesSchema); err != nil {
		db.Close()
		return nil, err
	}

	// Crear tabla routes para cachear rutas calculadas por OSRM
	routesSchema := `CREATE TABLE IF NOT EXISTS routes (
		key TEXT PRIMARY KEY,
		response BLOB,
		created_at TEXT
	);`

	if _, err := db.Exec(routesSchema); err != nil {
		db.Close()
		return nil, err
	}

	// Create table chats for storing conversation history
	chatsSchema := `CREATE TABLE IF NOT EXISTS chats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role TEXT,
		content TEXT,
		created_at TEXT
	);`
	if _, err := db.Exec(chatsSchema); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) SaveAlert(a processing.Alert) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO alerts(id, zone, type, severity, message, extract, timestamp) VALUES(?,?,?,?,?,?,?)`,
		a.ID, a.Zone, a.Type, a.Severity, a.Message, a.Extract, a.Timestamp.UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) ListAlerts() ([]processing.Alert, error) {
	rows, err := s.db.Query(`SELECT id, zone, type, severity, message, extract, timestamp FROM alerts ORDER BY timestamp DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []processing.Alert
	for rows.Next() {
		var a processing.Alert
		var ts string
		if err := rows.Scan(&a.ID, &a.Zone, &a.Type, &a.Severity, &a.Message, &a.Extract, &ts); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err == nil {
			a.Timestamp = t
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// SaveRoute guarda o reemplaza la respuesta de routing en la DB.
func (s *SQLiteStore) SaveRoute(key string, response []byte) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO routes(key, response, created_at) VALUES(?,?,?)`, key, response, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GetRoute devuelve la respuesta guardada y su timestamp (si existe).
func (s *SQLiteStore) GetRoute(key string) (response []byte, createdAt time.Time, found bool, err error) {
	row := s.db.QueryRow(`SELECT response, created_at FROM routes WHERE key = ?`, key)
	var ts string
	var resp []byte
	if err := row.Scan(&resp, &ts); err != nil {
		if err == sql.ErrNoRows {
			return nil, time.Time{}, false, nil
		}
		return nil, time.Time{}, false, err
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t = time.Time{}
	}
	return resp, t, true, nil
}

// ChatMessage representa una entrada de chat guardada.
type ChatMessage struct {
	ID        int64
	Role      string
	Content   string
	CreatedAt time.Time
}

// SaveChat guarda un mensaje de chat (usuario o bot).
func (s *SQLiteStore) SaveChat(role string, content string) error {
	_, err := s.db.Exec(`INSERT INTO chats(role, content, created_at) VALUES(?,?,?)`, role, content, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ListChats devuelve los mensajes de chat más recientes (limit mayor a 0)
func (s *SQLiteStore) ListChats(limit int) ([]ChatMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, role, content, created_at FROM chats ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChatMessage, 0)
	for rows.Next() {
		var cm ChatMessage
		var ts string
		if err := rows.Scan(&cm.ID, &cm.Role, &cm.Content, &ts); err != nil {
			return nil, err
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			cm.CreatedAt = t
		}
		out = append(out, cm)
	}
	return out, nil
}
