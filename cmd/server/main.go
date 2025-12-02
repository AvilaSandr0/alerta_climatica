package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"alerta_climatica/internal/processing"
	"alerta_climatica/internal/server"
	"alerta_climatica/internal/storage"
)

// Punto de entrada de la aplicación.
// Inicia el estado compartido, el procesador concurrente y el servidor HTTP.
func main() {
	// Inicializar almacenamiento SQLite (alerts.db en el working dir)
	store, err := storage.NewSQLite("alerts.db")
	if err != nil {
		log.Fatalf("no se pudo inicializar almacenamiento: %v", err)
	}
	defer store.Close()

	st := server.NewState(store)

	// Si la tabla zones está vacía, intentar importar un GeoJSON de paths conocidos
	if zlist, err := store.ListZones(); err == nil && len(zlist) == 0 {
		candidates := []string{"web/static/zones.geojson", "export.geojson", "../export.geojson"}
		for _, p := range candidates {
			if b, err := os.ReadFile(p); err == nil {
				if err := st.ImportZones(b); err != nil {
					log.Printf("warning: cannot import zones from %s: %v", p, err)
				} else {
					log.Printf("imported zones from %s", p)
					break
				}
			}
		}
	}

	// Configurar zonas simuladas (puedes ajustar o cargar de config en el futuro)
	zones := []string{"Zona Norte", "Zona Centro", "Zona Sur"}

	proc := processing.NewProcessor(zones, st.AddAlert)
	proc.StartWorkers(3) // 3 workers concurrentes (uno por zona simulada)

	srv := server.NewServer(st, proc)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	s := &http.Server{
		Addr:              addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Log PID to help correlate system events
	log.Printf("PID: %d", os.Getpid())

	// Señales para shutdown ordenado
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Ejecutar servidor en goroutine
	go func() {
		log.Printf("Servidor iniciado en http://localhost%v", addr)
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("error en ListenAndServe: %v", err)
		}
	}()

	// Esperar señal y loguear cuál fue
	sig := <-stop
	log.Printf("Shutdown: señal recibida: %v, iniciando cierre ordenado...", sig)

	// 1) Parar de aceptar nuevas conexiones
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		log.Printf("error durante server.Shutdown: %v", err)
	}

	// 2) Cerrar processor (espera a workers)
	proc.Close()

	// 3) Esperar que el state persista pendientes y cerrar store
	if err := st.Close(); err != nil {
		log.Printf("warning: error closing store: %v", err)
	}

	log.Println("Shutdown completo")
}
