package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
)

type FileItem struct {
	Type string `json:"type"` // "folder" or "file"
	Name string `json:"name"`
	Path string `json:"path"`
}

var rootPath string

func main() {
	// Parse command line arguments
	flag.StringVar(&rootPath, "path", ".", "Root path to serve files from")
	flag.Parse()

	// Convert to absolute path
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		log.Fatal("Invalid root path:", err)
	}
	rootPath = absPath

	log.Printf("Serving files from: %s", rootPath)

	// Create Fiber app
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			log.Printf("Error: %v", err)
			return c.Status(500).SendString("Internal Server Error")
		},
	})

	// Enable CORS
	app.Use(cors.New())

	// Serve the main HTML file at root
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendFile("./index.html")
	})

	// Image streaming route
	app.Get("/image/*", handleImageStream)

	// WebSocket upgrade middleware
	app.Use("/files", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			c.Locals("allowed", true)
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	// WebSocket handler
	app.Get("/files", websocket.New(handleWebSocket))

	// Start server
	log.Println("Server starting on :8080")
	log.Fatal(app.Listen(":8080"))
}

func handleImageStream(c *fiber.Ctx) error {
	// Get the path after /image/
	relativePath := c.Params("*")
	log.Printf("Image request for path: %s", relativePath)

	// Construct full path
	fullPath := filepath.Join(rootPath, relativePath)

	// Check if file exists
	info, err := os.Stat(fullPath)
	if err != nil {
		log.Printf("Image file does not exist: %s", fullPath)
		return c.Status(404).SendString("Image not found")
	}

	// Check if it's a file (not directory)
	if info.IsDir() {
		return c.Status(400).SendString("Path is a directory, not a file")
	}

	// Check if it's an image file
	ext := strings.ToLower(filepath.Ext(fullPath))
	if !isImageFile(ext) {
		return c.Status(400).SendString("File is not a supported image format")
	}

	// Set appropriate content type
	contentType := getImageContentType(ext)
	c.Set("Content-Type", contentType)

	// Stream the file
	return c.SendFile(fullPath)
}

func isImageFile(ext string) bool {
	imageExts := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
	}
	return imageExts[ext]
}

func getImageContentType(ext string) string {
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}

func handleWebSocket(c *websocket.Conn) {
	defer c.Close()

	// Get path from query parameters
	relativePath := c.Query("path", "")
	log.Printf("WebSocket connected for path: %s", relativePath)

	// Simply concatenate rootPath with relativePath
	fullPath := filepath.Join(rootPath, relativePath)

	// Check if path exists
	info, err := os.Stat(fullPath)
	if err != nil {
		log.Printf("Path does not exist: %s (error: %v)", fullPath, err)
		c.WriteJSON([]FileItem{}) // Send empty array and close
		return
	}

	if !info.IsDir() {
		log.Printf("Path is not a directory: %s", fullPath)
		c.WriteJSON([]FileItem{}) // Send empty array and close
		return
	}

	// List directory contents
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		log.Printf("Error reading directory %s: %v", fullPath, err)
		c.WriteJSON([]FileItem{}) // Send empty array and close
		return
	}

	// Convert to FileItems
	var folders []FileItem
	var files []FileItem

	for _, entry := range entries {
		// Skip hidden files/folders
		if entry.Name()[0] == '.' {
			continue
		}

		// Create relative path for the item by appending entry name to current relative path
		var itemRelativePath string
		if relativePath == "" {
			itemRelativePath = entry.Name()
		} else {
			itemRelativePath = filepath.Join(relativePath, entry.Name())
		}
		// Normalize path separators for web
		itemRelativePath = filepath.ToSlash(itemRelativePath)

		item := FileItem{
			Name: entry.Name(),
			Path: itemRelativePath,
		}

		if entry.IsDir() {
			item.Type = "folder"
			folders = append(folders, item)
		} else {
			item.Type = "file"
			files = append(files, item)
		}
	}

	// Combine folders first, then files
	allItems := append(folders, files...)

	// Send items in chunks of 10
	chunkSize := 10
	for i := 0; i < len(allItems); i += chunkSize {
		end := i + chunkSize
		if end > len(allItems) {
			end = len(allItems)
		}

		chunk := allItems[i:end]
		
		// Send chunk
		if err := c.WriteJSON(chunk); err != nil {
			log.Printf("Error sending chunk: %v", err)
			return
		}

		// Add small delay to simulate real-world loading
		time.Sleep(100 * time.Millisecond)
	}

	// Send empty array to indicate completion
	if err := c.WriteJSON([]FileItem{}); err != nil {
		log.Printf("Error sending completion signal: %v", err)
	}

	log.Printf("Finished sending files for path: %s", relativePath)
}


