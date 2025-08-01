package main

import (
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/websocket/v2"
	"github.com/otiai10/copy"
	"github.com/tus/tusd/pkg/filestore"
	"github.com/tus/tusd/pkg/handler"

	// "github.com/tus/tusd/pkg/filestore"
	// "github.com/tus/tusd/pkg/handler"

	// "github.com/tus/tusd/v2/pkg/filestore"
	// "github.com/tus/tusd/v2/pkg/handler"

	"net/http"
	"net/url"
)

func copyFile(src, dst string) error {
	return copy.Copy(src, dst)
}

func moveFile(src, dst string) error {
	return os.Rename(src, dst)
}

func copyDir(src, dst string) error {
	return copy.Copy(src, dst)
}

func moveDir(src, dst string) error {
	return os.Rename(src, dst)
}

type DocumentData struct {
	Title        string
	DocumentName string
	Content      template.HTML
}

type IndexData struct {
	WriteMode bool // Changed to WriteMode
}

func handleManage(c *fiber.Ctx) error {
	// Add this check at the beginning
	if !writeMode {
		return c.Status(403).JSON(fiber.Map{
			"status": "error",
			"error":  "File operations are disabled. Use --write flag to enable write mode",
		})
	}

	// Get parameters
	sources := c.Query("srcs")
	action := c.Query("action")
	dest := c.Query("dest", "")

	if sources == "" || action == "" {
		return c.Status(400).JSON(fiber.Map{
			"status": "error",
			"error":  "Missing required parameters: srcs and action",
		})
	}

	// Parse sources (they come as multiple values with same key)
	query := string(c.Request().URI().QueryString())
	values, _ := url.ParseQuery(query)
	srcList := values["srcs"] // This returns []string
	if len(srcList) == 0 {
		return c.Status(400).JSON(fiber.Map{
			"status": "error",
			"error":  "No source files provided",
		})
	}

	// Validate action
	if action != "copy" && action != "paste" {
		return c.Status(400).JSON(fiber.Map{
			"status": "error",
			"error":  "Invalid action. Must be 'copy' or 'paste'",
		})
	}

	// Build destination path
	destPath := filepath.Join(rootPath, dest)

	// Check if destination exists and is a directory
	destInfo, err := os.Stat(destPath)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"status": "error",
			"error":  "Destination path does not exist",
		})
	}

	if !destInfo.IsDir() {
		return c.Status(400).JSON(fiber.Map{
			"status": "error",
			"error":  "Destination must be a directory",
		})
	}

	var errors []string

	// Process each source file
	for _, src := range srcList {
		srcPath := filepath.Join(rootPath, src)

		// Check if source exists
		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to %s %s to %s: source does not exist", action, src, dest))
			continue
		}

		// Get the base name for the destination
		baseName := filepath.Base(srcPath)
		targetPath := filepath.Join(destPath, baseName)

		// Perform the operation
		if srcInfo.IsDir() {
			// Handle directory
			if action == "copy" {
				log.Printf("Would COPY DIR: %s -> %s", srcPath, targetPath)
				err = copyDir(srcPath, targetPath)
			} else { // paste (move)
				log.Printf("Would MOVE DIR: %s -> %s", srcPath, targetPath)
				err = moveDir(srcPath, targetPath)
			}
		} else {
			// Handle file
			if action == "copy" {
				log.Printf("Would COPY FILE: %s -> %s", srcPath, targetPath)
				err = copyFile(srcPath, targetPath)
			} else { // paste (move)
				log.Printf("Would MOVE FILE: %s -> %s", srcPath, targetPath)
				err = moveFile(srcPath, targetPath)
			}
		}

		// Comment out error handling since we're not actually doing operations
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to %s %s to %s: %v", action, src, dest, err))
		}
	}

	// Return response
	if len(errors) > 0 {
		return c.JSON(fiber.Map{
			"status": "error",
			"error":  strings.Join(errors, "; "),
		})
	}

	return c.JSON(fiber.Map{
		"status": "ok",
	})
}

type FileItem struct {
	Type string `json:"type"` // "folder", "file", "image", or "document"
	Name string `json:"name"`
	Path string `json:"path"`
}

func handleDocument(c *fiber.Ctx) error {
	// Check if office docs are enabled
	if libreOfficeAppPath == "" {
		return c.Status(503).SendString("Office document viewing is not enabled.")
	}

	// Get document path from query parameter and decode it
	encodedDocPath := c.Query("path")
	if encodedDocPath == "" {
		return c.Status(400).SendString("Document path is required")
	}

	// Decode the URL-encoded path
	decodedDocPath, err := url.QueryUnescape(encodedDocPath)
	if err != nil {
		return c.Status(400).SendString("Invalid document path encoding")
	}

	// Concatenate with root path to get full file path
	fullDocPath := filepath.Join(rootPath, decodedDocPath)

	// Check if file exists
	if _, err := os.Stat(fullDocPath); os.IsNotExist(err) {
		return c.Status(404).SendString("File not found: " + decodedDocPath)
	}

	// Parse the template from file
	tmpl, err := template.ParseFiles("doc_viewer.html.tmpl")
	if err != nil {
		return c.Status(500).SendString("Template error: " + err.Error())
	}

	// Convert document to HTML using LibreOffice
	htmlContent, err := convertDocumentToHTML(fullDocPath)
	if err != nil {
		return c.Status(500).SendString("Document conversion failed: " + err.Error())
	}

	// Prepare template data
	data := DocumentData{
		Title:        decodedDocPath,
		DocumentName: decodedDocPath,
		Content:      template.HTML(htmlContent),
	}

	// Execute the template
	c.Set("Content-Type", "text/html")
	return tmpl.Execute(c.Response().BodyWriter(), data)
}

func convertDocumentToHTML(docPath string) (string, error) {
	// Create temporary directory for output
	tempDir, err := ioutil.TempDir("", "libreoffice_convert_")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir) // Clean up temp directory

	// Determine the file extension to choose appropriate filter
	ext := strings.ToLower(filepath.Ext(docPath))
	var convertFilter string

	switch ext {
	case ".docx", ".doc", ".odt", ".rtf":
		convertFilter = "html:XHTML Writer File:BodyOnly,EmbedImages"
	case ".xlsx", ".xls", ".ods":
		convertFilter = "html:HTML (StarCalc):EmbedImages:BodyOnly"
	case ".pptx", ".ppt", ".odp":
		convertFilter = "html:HTML (Impress):EmbedImages:BodyOnly"
	default:
		return "", fmt.Errorf("unsupported file format: %s", ext)
	}

	// Prepare LibreOffice command
	cmd := exec.Command(
		libreOfficeAppPath,
		"--headless",
		"--convert-to", convertFilter,
		"--outdir", tempDir,
		docPath,
	)

	// Execute the conversion
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("LibreOffice conversion failed: %v, output: %s", err, string(output))
	}

	// Determine the output HTML filename
	baseName := filepath.Base(docPath)
	nameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	htmlFileName := nameWithoutExt + ".html"
	htmlFilePath := filepath.Join(tempDir, htmlFileName)

	// Read the generated HTML file
	htmlContent, err := ioutil.ReadFile(htmlFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to read converted HTML file: %v", err)
	}

	return string(htmlContent), nil
}

var (
	rootPath           string
	libreOfficeAppPath string
	writeMode          bool
	// Version information - these will be set at build time
	version   = "0.1.0-alpha" // Default version
	buildDate = "unknown"     // Will be set during build
	gitCommit = "unknown"     // Will be set during build
)

// func setupTusUpload(app *fiber.App) {
// 	if !writeMode {
// 		return
// 	}

// 	// Create file store
// 	store := filestore.New("./temp_uploads")

// 	// Create composer
// 	composer := handler.NewStoreComposer()
// 	store.UseIn(composer)

// 	// Create handler
// 	tusHandler, err := handler.NewHandler(handler.Config{
// 		BasePath:                "/upload/tus/",
// 		StoreComposer:           composer,
// 		NotifyCompleteUploads:   true,
// 		RespectForwardedHeaders: true,
// 	})
// 	if err != nil {
// 		log.Printf("Unable to create TUS handler: %s", err)
// 		return
// 	}

// 	// Handle completed uploads
// 	go func() {
// 		for event := range tusHandler.CompleteUploads {
// 			// Fix: Use the store directly instead of tusHandler.DataStore
// 			info := event.Upload
// 			targetPath := info.MetaData["relativePath"]
// 			filename := info.MetaData["filename"]

// 			tempFile := filepath.Join("./temp_uploads", event.Upload.ID)
// 			finalPath := filepath.Join(rootPath, targetPath, filename)

// 			os.MkdirAll(filepath.Dir(finalPath), 0755)
// 			os.Rename(tempFile, finalPath)
// 		}
// 	}()

// 	// Mount handler - simplified version
// 	app.All("/upload/tus/*", func(c *fiber.Ctx) error {
// 		// Convert fasthttp request to net/http request
// 		// req := &http.Request{
// 		// 	Method: c.Method(),
// 		// 	URL:    &url.URL{Path: string(c.Request().URI().Path())},
// 		// 	Header: make(http.Header),
// 		// 	Body:   io.NopCloser(c.Request().BodyStream()), // Fix: wrap with NopCloser
// 		// }

// 		req, err := adaptor.ConvertRequest(c, true) // or false, depending on your use-case
// 		if err != nil {
// 			return err
// 		}

// 		// Copy headers
// 		c.Request().Header.VisitAll(func(key, value []byte) {
// 			req.Header.Set(string(key), string(value))
// 		})

// 		w := &httpResponseWriter{ctx: c}
// 		tusHandler.ServeHTTP(w, req)
// 		return nil
// 	})
// }

// func setupResumableUpload(app *fiber.App) {
// 	if !writeMode {
// 		return
// 	}

// 	// Create resumable uploader
// 	resumable := go_resumable.NewResumable("./temp_uploads")

// 	// Handle upload chunks
// 	app.Post("/upload/resumable", func(c *fiber.Ctx) error {
// 		// Convert fiber request to standard http request
// 		req, err := adaptor.ConvertRequest(c, true)
// 		if err != nil {
// 			return err
// 		}

// 		w := &httpResponseWriter{ctx: c}

// 		// Handle the chunk
// 		resumable.Handle(w, req)
// 		return nil
// 	})

// 	// Handle upload completion
// 	resumable.OnComplete(func(filename string, tempPath string) {
// 		// Get target path from query params (you'll need to store this)
// 		targetPath := "" // You might need to store this in metadata
// 		finalPath := filepath.Join(rootPath, targetPath, filename)

// 		os.MkdirAll(filepath.Dir(finalPath), 0755)
// 		os.Rename(tempPath, finalPath)

// 		log.Printf("Upload completed: %s", finalPath)
// 	})
// }

func setupTusUpload(app *fiber.App) {
	if !writeMode {
		return
	}

	// Create file store
	store := filestore.New("./uploads")

	// Create composer
	composer := handler.NewStoreComposer()
	store.UseIn(composer)

	// Create config
	config := handler.Config{
		StoreComposer:         composer,
		NotifyCompleteUploads: true,
		BasePath:              "/upload/tus/",
	}

	// Create handler
	tusHandler, err := handler.NewHandler(config)
	if err != nil {
		log.Printf("Unable to create TUS handler: %s", err)
		return
	}

	// Handle completed uploads
	go func() {
		for event := range tusHandler.CompleteUploads {
			info := event.Upload
			targetPath := info.MetaData["relativePath"]
			filename := info.MetaData["filename"]

			tempFile := filepath.Join("./uploads", event.Upload.ID)
			finalPath := filepath.Join(rootPath, targetPath, filename)
			log.Printf("Final path: %s", finalPath)
			os.MkdirAll(filepath.Dir(finalPath), 0755)
			//os.Rename(tempFile, finalPath)
			copyFile(tempFile, finalPath)
			log.Printf("Successfully moved %s to %s", tempFile, finalPath)
		}
	}()

	// Mount using the bridge pattern - no manual conversion needed!
	prefix := "/upload/tus/"
	group := app.Group(prefix, adaptor.HTTPMiddleware(tusHandler.Middleware))

	group.Post("", adaptor.HTTPHandlerFunc(tusHandler.PostFile))
	group.Head(":id", adaptor.HTTPHandlerFunc(tusHandler.HeadFile))
	group.Patch(":id", adaptor.HTTPHandlerFunc(tusHandler.PatchFile))
	group.Get(":id", adaptor.HTTPHandlerFunc(tusHandler.GetFile))
	group.Delete(":id", adaptor.HTTPHandlerFunc(tusHandler.DelFile))
}

// HTTP response writer adapter
type httpResponseWriter struct {
	ctx *fiber.Ctx
}

func (w *httpResponseWriter) Header() http.Header {
	headers := make(http.Header)
	w.ctx.Response().Header.VisitAll(func(key, value []byte) {
		headers.Set(string(key), string(value))
	})
	return headers
}

func (w *httpResponseWriter) Write(data []byte) (int, error) {
	return w.ctx.Response().BodyWriter().Write(data)
}

func (w *httpResponseWriter) WriteHeader(statusCode int) {
	w.ctx.Status(statusCode)
}

func main() {
	// Parse command line arguments
	var showVersion bool
	flag.BoolVar(&showVersion, "version", false, "Show version information and exit")
	flag.StringVar(&rootPath, "path", ".", "Root path to serve files from")
	flag.StringVar(&libreOfficeAppPath, "libreoffice", "", "Path to LibreOffice AppImage executable (optional - enables office document viewing)")
	flag.BoolVar(&writeMode, "write", false, "Enable write mode (allows file operations)")
	flag.Parse()

	// Handle version flag
	if showVersion {
		fmt.Printf("doc-viewer version %s\n", version)
		fmt.Printf("Build date: %s\n", buildDate)
		fmt.Printf("Git commit: %s\n", gitCommit)
		return
	}

	// Validate LibreOffice path if provided
	if libreOfficeAppPath != "" {
		if _, err := os.Stat(libreOfficeAppPath); os.IsNotExist(err) {
			log.Printf("LibreOffice AppImage not found at: %s - resetting to disabled", libreOfficeAppPath)
			libreOfficeAppPath = ""
		}
	}

	// Print final LibreOffice path status
	if libreOfficeAppPath != "" {
		log.Printf("LibreOffice path: %s", libreOfficeAppPath)
	} else {
		log.Printf("LibreOffice path: (not set - office document viewing disabled)")
	}

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

	// Serve static files from ./static directory
	app.Static("/static", "./static")

	// Your existing server setup code here...
	app.Get("/doc_viewer", handleDocument)

	// Serve the main HTML file at root
	app.Get("/", func(c *fiber.Ctx) error {
		tmpl, err := template.ParseFiles("./index.html.tmpl")
		if err != nil {
			return c.Status(500).SendString("Template error: " + err.Error())
		}

		data := IndexData{
			WriteMode: writeMode, // Pass writeMode
		}

		c.Set("Content-Type", "text/html")
		return tmpl.Execute(c.Response().BodyWriter(), data)
	})

	// Serve the document viewer HTML file
	app.Get("/doc_viewer", func(c *fiber.Ctx) error {
		return c.SendFile("./doc_viewer.html")
	})

	// Image streaming route - now uses query parameter
	app.Get("/image", handleImageStream)

	// File streaming route - now uses query parameter
	app.Get("/file", handleFileStream)

	//
	app.Get("/manage", handleManage)

	// WebSocket upgrade middleware
	app.Use("/files", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			c.Locals("allowed", true)
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})

	setupTusUpload(app)
	// WebSocket handler
	app.Get("/files", websocket.New(handleWebSocket))

	// Start server
	log.Println("Server starting on :8080")
	log.Println("Static files served from: ./static")
	log.Fatal(app.Listen(":8080"))
}

func handleImageStream(c *fiber.Ctx) error {
	// Get the path from query parameter
	relativePath := c.Query("path")
	if relativePath == "" {
		return c.Status(400).SendString("Path parameter required")
	}

	// Explicitly URL decode the path
	decodedPath, err := url.QueryUnescape(relativePath)
	if err != nil {
		log.Printf("Error decoding path: %v", err)
		return c.Status(400).SendString("Invalid path encoding")
	}

	log.Printf("Image request for path: %s", decodedPath)

	// Construct full path using decoded path
	fullPath := filepath.Join(rootPath, decodedPath)

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

func handleFileStream(c *fiber.Ctx) error {
	// Get the path from query parameter
	relativePath := c.Query("path")
	if relativePath == "" {
		return c.Status(400).SendString("Path parameter required")
	}

	// Explicitly URL decode the path
	decodedPath, err := url.QueryUnescape(relativePath)
	if err != nil {
		log.Printf("Error decoding path: %v", err)
		return c.Status(400).SendString("Invalid path encoding")
	}

	log.Printf("File request for path: %s", decodedPath)

	// Construct full path using decoded path
	fullPath := filepath.Join(rootPath, decodedPath)

	// Check if file exists
	info, err := os.Stat(fullPath)
	if err != nil {
		log.Printf("File does not exist: %s", fullPath)
		return c.Status(404).SendString("File not found")
	}

	// Check if it's a file (not directory)
	if info.IsDir() {
		return c.Status(400).SendString("Path is a directory, not a file")
	}

	// Set appropriate content type
	ext := strings.ToLower(filepath.Ext(fullPath))
	contentType := getFileContentType(ext)
	c.Set("Content-Type", contentType)

	// Set Content-Disposition header for documents to suggest download
	if isDocumentFile(ext) {
		filename := filepath.Base(fullPath)
		c.Set("Content-Disposition", "inline; filename=\""+filename+"\"")
	}

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

func isDocumentFile(ext string) bool {
	docExts := map[string]bool{
		".docx": true,
		".doc":  true,
		".xls":  true,
		".xlsx": true,
		".ppt":  true,
		".pptx": true,
		".pdf":  true,
	}
	return docExts[ext]
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

func getFileContentType(ext string) string {
	switch ext {
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".doc":
		return "application/msword"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".js":
		return "application/javascript"
	case ".css":
		return "text/css"
	case ".html":
		return "text/html"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func getFileType(entry os.DirEntry) string {
	if entry.IsDir() {
		return "folder"
	}

	ext := strings.ToLower(filepath.Ext(entry.Name()))
	if isImageFile(ext) {
		return "image"
	} else if isDocumentFile(ext) {
		return "document"
	}
	return "file"
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
	var images []FileItem
	var documents []FileItem
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

		fileType := getFileType(entry)
		item := FileItem{
			Type: fileType,
			Name: entry.Name(),
			Path: itemRelativePath,
		}

		switch fileType {
		case "folder":
			folders = append(folders, item)
		case "image":
			images = append(images, item)
		case "document":
			documents = append(documents, item)
		default:
			files = append(files, item)
		}
	}

	// Combine in order: folders, images, documents, then other files
	allItems := append(folders, images...)
	allItems = append(allItems, documents...)
	allItems = append(allItems, files...)

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
	}

	// Send empty array to indicate completion
	if err := c.WriteJSON([]FileItem{}); err != nil {
		log.Printf("Error sending completion signal: %v", err)
	}

	log.Printf("Finished sending files for path: %s", relativePath)
}
