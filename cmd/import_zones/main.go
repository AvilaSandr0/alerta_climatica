package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"alerta_climatica/internal/storage"
)

func main() {
	// open DB (same file used by server)
	store, err := storage.NewSQLite("alerts.db")
	if err != nil {
		log.Fatalf("failed opening sqlite store: %v", err)
	}
	defer store.Close()

	// read geojson (export.geojson at repo root)
	f, err := os.Open("export.geojson")
	if err != nil {
		log.Fatalf("cannot open export.geojson: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		log.Fatalf("cannot read export.geojson: %v", err)
	}

	if err := store.ImportZonesFromGeoJSON(data); err != nil {
		log.Fatalf("import failed: %v", err)
	}

	zones, err := store.ListZones()
	if err != nil {
		log.Fatalf("list zones failed: %v", err)
	}

	fmt.Printf("Imported %d zones:\n", len(zones))
	for _, z := range zones {
		fmt.Printf("- %d: %s\n", z.ID, z.Name)
	}
}
