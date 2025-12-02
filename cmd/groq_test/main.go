package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"alerta_climatica/internal/integrations/groq"
)

func main() {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Println("GROQ_API_KEY not set")
		os.Exit(2)
	}
	msgs := []groq.Message{{Role: "user", Content: "Prueba de conexión desde test local"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply, err := groq.Chat(ctx, apiKey, "openai/gpt-oss-120b", msgs)
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	fmt.Println("REPLY:")
	fmt.Println(reply)
}
