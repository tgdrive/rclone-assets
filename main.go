package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "github.com/rclone/rclone/backend/local"
	_ "github.com/rclone/rclone/backend/teldrive"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"
)

const (
	MAX_UPLOAD_SIZE    = 50 << 20
	ALLOWED_DOMAINS    = "*"
	DIR_SHARDING_DEPTH = 1
	FILES_PER_DIR      = 5000
)

var (
	DATABASE_URL       string
	RCLONE_REMOTE_PATH string
	API_KEY            string
	CACHE_DIR          string
	PORT               string
)

func init() {

	DATABASE_URL = getEnv("DATABASE_URL", "")
	RCLONE_REMOTE_PATH = getEnv("RCLONE_REMOTE_PATH", "")
	API_KEY = getEnv("API_KEY", "")
	CACHE_DIR = getEnv("CACHE_DIR", "/var/cache")
	PORT = getEnv("PORT", "8080")

	if DATABASE_URL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	if RCLONE_REMOTE_PATH == "" {
		log.Fatal("RCLONE_REMOTE_PATH environment variable is required (e.g., 's3:bucket/path')")
	}
	if API_KEY == "" {
		log.Fatal("API_KEY environment variable is required")
	}

}

type Asset struct {
	ID        string    `json:"id" gorm:"type:varchar(20);primary_key"`
	FileName  string    `json:"fileName,omitempty" gorm:"not null"`
	Size      int64     `json:"size" gorm:"not null"`
	MimeType  string    `json:"mimeType" gorm:"not null"`
	Hash      string    `json:"hash,omitempty" gorm:"uniqueIndex:idx_assets_hash;not null"`
	CreatedAt time.Time `json:"createdAt" gorm:"autoCreateTime;index:idx_assets_created_at"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"autoUpdateTime"`
}

type AssetService struct {
	db  *gorm.DB
	fs  fs.Fs
	vfs *vfs.VFS
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

var assetService *AssetService

func main() {

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	defer cancel()

	configfile.Install()

	f, err := fs.NewFs(ctx, RCLONE_REMOTE_PATH)

	if err != nil {
		log.Fatalf("Failed to initialize Rclone backend for path '%s': %v", RCLONE_REMOTE_PATH, err)
	}
	log.Printf("Rclone backend initialized: %s (%s)", f.Name(), f.String())

	vfsOpts := vfscommon.Opt
	vfsOpts.CacheMode = vfscommon.CacheModeFull

	dirCacheEnv := getEnv("DIR_CACHE_TIME", "60m")
	if err := vfsOpts.DirCacheTime.Set(dirCacheEnv); err == nil {
		_ = vfsOpts.DirCacheTime.Set("60m")
	}
	maxAgeEnv := getEnv("CACHE_MAX_AGE", "24h")
	if err := vfsOpts.CacheMaxAge.Set(maxAgeEnv); err == nil {
		_ = vfsOpts.CacheMaxAge.Set("24h")
	}

	cacheMaxSizeEnv := getEnv("CACHE_MAX_SIZE", "10G")
	if err := vfsOpts.CacheMaxSize.Set(cacheMaxSizeEnv); err != nil {
		_ = vfsOpts.CacheMaxSize.Set("10G")
	}

	if err := os.MkdirAll(CACHE_DIR, 0755); err != nil {
		log.Printf("Warning: Failed to create cache dir: %v", err)
		os.Exit(1)
	}

	config.SetCacheDir(CACHE_DIR)

	vfsObj := vfs.New(f, &vfsOpts)

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  DATABASE_URL,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if err := db.AutoMigrate(&Asset{}); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	assetService = &AssetService{
		db:  db,
		fs:  f,
		vfs: vfsObj,
	}

	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{ALLOWED_DOMAINS},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "X-API-Key", "Content-Disposition"},
		ExposeHeaders:    []string{"Content-Length", "Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	api := router.Group("/")
	api.PUT("/upload", APIKeyAuth(), assetService.handleRawUpload)
	api.GET("/assets", APIKeyAuth(), assetService.listAssets)
	api.DELETE("/assets/:id", APIKeyAuth(), assetService.deleteAsset)
	api.GET("/assets/:name", assetService.downloadAsset)

	srv := &http.Server{
		Addr:    ":" + PORT,
		Handler: router,
	}

	go func() {
		log.Printf("Starting asset API server on port %s", PORT)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	<-ctx.Done()

	log.Println("Shutting down server...")

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}
	log.Println("Server exiting")
}

func APIKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey != API_KEY {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized: Invalid API key",
			})
			return
		}
		c.Next()
	}
}

func ErrorResponse(code int, message string) gin.H {
	return gin.H{
		"success": false,
		"error": gin.H{
			"code":    code,
			"message": message,
		},
	}
}

func (s *AssetService) getStoragePath(hash string) string {
	if len(hash) < 2 {
		return hash
	}
	return filepath.Join(hash[0:2], hash)
}

func (s *AssetService) saveAssetMetadata(asset *Asset) error {
	result := s.db.Create(asset)
	return result.Error
}

func (s *AssetService) getAssetByID(id string) (*Asset, error) {
	var asset Asset
	result := s.db.First(&asset, "id = ?", id)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, result.Error
	}

	return &asset, nil
}

func (s *AssetService) deleteAssetMetadata(id string) error {
	result := s.db.Delete(&Asset{}, "id = ?", id)
	return result.Error
}

func (s *AssetService) getAssetsByPage(limit, offset int) ([]Asset, error) {
	var assets []Asset
	result := s.db.Model(&Asset{}).Order("created_at DESC").Limit(limit).Offset(offset).Find(&assets)
	return assets, result.Error
}

func (s *AssetService) countAssets() (int64, error) {
	var count int64
	result := s.db.Model(&Asset{}).Count(&count)
	return count, result.Error
}

func (s *AssetService) handleRawUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MAX_UPLOAD_SIZE)

	// 1. Save to local temp file to calculate hash and size safely
	tempFile, err := os.CreateTemp("", "upload-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to create temp file"))
		return
	}
	defer os.Remove(tempFile.Name()) // Clean up temp file
	defer tempFile.Close()

	// Read header to detect mime
	headerBuffer := make([]byte, 512)
	n, err := c.Request.Body.Read(headerBuffer)
	if err != nil && err != io.EOF {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to read request body"))
		return
	}

	mtype := mimetype.Detect(headerBuffer[:n])

	// Reconstruct reader
	bodyReader := io.MultiReader(bytes.NewReader(headerBuffer[:n]), c.Request.Body)
	hashWriter := md5.New()
	teeReader := io.TeeReader(bodyReader, hashWriter)

	size, err := io.Copy(tempFile, teeReader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to write temp file"))
		return
	}

	// Reset temp file for reading
	tempFile.Seek(0, 0)
	fileHash := hex.EncodeToString(hashWriter.Sum(nil))
	assetID := xid.New().String()

	// CAS Logic: Check if hash already exists
	var existingAsset Asset
	if err := s.db.Where("hash = ?", fileHash).First(&existingAsset).Error; err == nil {
		// Found existing asset - Deduplicate
		log.Printf("Asset deduplicated: Returning existing ID %s for hash %s", existingAsset.ID, fileHash)
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"asset":   existingAsset,
			"deduped": true,
		})
		return
	}

	// 2. Upload to Rclone (Only if new)
	shardedPath := s.getStoragePath(fileHash)
	// Ensure standard path separator
	remoteFilePath := shardedPath
	if os.PathSeparator == '\\' {
		remoteFilePath = strings.ReplaceAll(remoteFilePath, "\\", "/")
	}

	ctx := c.Request.Context()
	objInfo := object.NewStaticObjectInfo(remoteFilePath, time.Now(), size, true, nil, s.fs)

	_, err = s.fs.Put(ctx, tempFile, objInfo)
	if err != nil {
		log.Printf("Rclone upload failed: %v", err)
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to upload to storage backend"))
		return
	}

	// 3. Save Metadata
	asset := &Asset{
		ID:       assetID,
		FileName: assetID + mtype.Extension(), // Logical filename
		Size:     size,
		MimeType: mtype.String(),
		Hash:     fileHash,
	}

	if err := assetService.saveAssetMetadata(asset); err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to save asset metadata"))
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"asset":   *asset,
	})
}

func (s *AssetService) listAssets(c *gin.Context) {
	limit := 100
	offset := 0

	if limitParam := c.DefaultQuery("limit", ""); limitParam != "" {
		parsedLimit, err := strconv.Atoi(limitParam)
		if err == nil && parsedLimit > 0 && parsedLimit <= 1000 {
			limit = parsedLimit
		}
	}
	if offsetParam := c.DefaultQuery("offset", ""); offsetParam != "" {
		parsedOffset, err := strconv.Atoi(offsetParam)
		if err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}
	totalCount, err := assetService.countAssets()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to count assets"))
		return
	}
	assets, err := assetService.getAssetsByPage(limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to list assets"))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"total":   totalCount,
		"limit":   limit,
		"offset":  offset,
		"assets":  assets,
	})
}

func (s *AssetService) downloadAsset(c *gin.Context) {
	assetName := c.Param("name")
	assetID := strings.Split(assetName, ".")[0]
	if _, err := xid.FromString(assetID); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse(400, "Invalid asset ID format"))
		return
	}
	asset, err := assetService.getAssetByID(assetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to retrieve asset"))
		return
	}

	if asset == nil {
		c.JSON(http.StatusNotFound, ErrorResponse(404, "Asset not found"))
		return
	}

	// Construct path from hash (CAS)
	remoteFilePath := s.getStoragePath(asset.Hash)
	if os.PathSeparator == '\\' {
		remoteFilePath = strings.ReplaceAll(remoteFilePath, "\\", "/")
	}

	// Get Object from Rclone VFS (Cached)
	// We use the VFS layer here to ensure the file is cached locally on access.
	// 1. Get File Handle (triggers download to cache if needed)
	fHandle, err := s.vfs.OpenFile(remoteFilePath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, ErrorResponse(404, "Asset file not found on storage"))
		} else {
			c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to open cached file"))
		}
		return
	}
	defer fHandle.Close()

	// 2. Get Info
	fInfo, err := fHandle.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to stat cached file"))
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%s", assetName))
	c.Header("Content-Length", fmt.Sprintf("%d", fInfo.Size()))
	c.Header("Content-Type", asset.MimeType)
	c.Header("Cache-Control", "max-age=2592000")

	// 3. Stream from Cache
	_, err = io.Copy(c.Writer, fHandle)
	if err != nil {
		log.Printf("Stream error: %v", err)
	}
}

func (s *AssetService) deleteAsset(c *gin.Context) {
	assetID := c.Param("id")

	if _, err := xid.FromString(assetID); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse(400, "Invalid asset ID format"))
		return
	}

	asset, err := assetService.getAssetByID(assetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to retrieve asset"))
		return
	}

	if asset == nil {
		c.JSON(http.StatusNotFound, ErrorResponse(404, "Asset not found"))
		return
	}

	// CAS Delete Logic (Strict 1:1 Hash Mapping):
	// Since Hash is unique, deleting this asset means we definitely delete the file.

	// 1. Delete Metadata
	if err := s.db.Delete(&Asset{}, "id = ?", assetID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to delete asset metadata"))
		return
	}

	// 2. Delete Physical File
	remoteFilePath := s.getStoragePath(asset.Hash)
	if os.PathSeparator == '\\' {
		remoteFilePath = strings.ReplaceAll(remoteFilePath, "\\", "/")
	}

	obj, err := s.fs.NewObject(c.Request.Context(), remoteFilePath)
	if err == nil {
		if err := obj.Remove(c.Request.Context()); err != nil {
			log.Printf("Warning: Failed to remove physical file %s: %v", remoteFilePath, err)
		} else {
			log.Printf("Physical file deleted: %s", remoteFilePath)
		}
	} else if err != fs.ErrorObjectNotFound {
		log.Printf("Warning: Error finding file to delete %s: %v", remoteFilePath, err)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}
