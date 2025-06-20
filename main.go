package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/rs/xid"
	"golang.org/x/sync/errgroup"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	PORT               = "8080"
	MAX_UPLOAD_SIZE    = 50 << 20
	ALLOWED_DOMAINS    = "*"
	DIR_SHARDING_DEPTH = 1
	FILES_PER_DIR      = 5000
)

var (
	DATABASE_URL = getEnv("DATABASE_URL", "")
	STORAGE_PATH = getEnv("STORAGE_PATH", "/app/rclone-assets")
	API_KEY      = getEnv("API_KEY", "")
)

type Asset struct {
	ID          string    `json:"id" gorm:"type:varchar(20);primary_key"`
	StoragePath string    `json:"-" gorm:"not null"`
	FileName    string    `json:"fileName,omitempty" gorm:"not null"`
	Size        int64     `json:"size" gorm:"not null"`
	MimeType    string    `json:"mimeType" gorm:"not null"`
	Hash        string    `json:"hash,omitempty" gorm:"index"`
	CreatedAt   time.Time `json:"createdAt" gorm:"autoCreateTime"`
	UpdatedAt   time.Time `json:"updatedAt" gorm:"autoUpdateTime"`
}

type AssetService struct {
	db               *gorm.DB
	mu               sync.Mutex
	directoryCounter *sync.Map
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

var assetService *AssetService

func main() {
	if DATABASE_URL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	if STORAGE_PATH == "" {
		log.Fatal("STORAGE_PATH environment variable is required")
	}
	if API_KEY == "" {
		log.Fatal("API_KEY environment variable is required")
	}

	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logger.Error,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  DATABASE_URL,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: gormLogger,
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
		db:               db,
		directoryCounter: &sync.Map{},
	}

	if err := assetService.initDirectoryCounters(); err != nil {
		log.Printf("Warning: Failed to initialize directory counters: %v", err)
	}

	if _, err := os.Stat(STORAGE_PATH); os.IsNotExist(err) {
		log.Fatal("rClone mount path does not exist: ", STORAGE_PATH)
	}

	router := gin.Default()

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{ALLOWED_DOMAINS},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "X-API-Key", "Content-Disposition"},
		ExposeHeaders:    []string{"Content-Length", "Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	router.GET("/health", healthCheck)

	api := router.Group("/")
	api.PUT("/upload", APIKeyAuth(), assetService.handleRawUpload)
	api.GET("/assets", APIKeyAuth(), assetService.listAssets)
	api.DELETE("/assets/:id", APIKeyAuth(), assetService.deleteAsset)
	api.GET("/assets/:name", assetService.downloadAsset)

	log.Printf("Starting asset API server on port %s", PORT)
	if err := router.Run(":" + PORT); err != nil {
		log.Fatal("Failed to start server: ", err)
	}
}
func (s *AssetService) initDirectoryCounters() error {
	var assets []Asset
	result := s.db.Select("storage_path").Find(&assets)
	if result.Error != nil {
		return result.Error
	}

	var g errgroup.Group

	g.SetLimit(8)

	for _, asset := range assets {
		g.Go(func() error {
			dir := filepath.Dir(asset.StoragePath)
			actual, _ := s.directoryCounter.LoadOrStore(dir, new(atomic.Int64))
			counter := actual.(*atomic.Int64)
			counter.Add(1)
			return nil
		})
	}
	g.Wait()
	numDirectories := 0
	s.directoryCounter.Range(func(key, value any) bool {
		numDirectories++
		return true
	})

	log.Printf("Initialized directory counters, tracking %d directories", numDirectories)
	return nil
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

func healthCheck(c *gin.Context) {
	sqlDB, err := assetService.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"time":   time.Now().Format(time.RFC3339),
			"error":  "Database connection failed",
		})
		return
	}

	if err := sqlDB.Ping(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"time":   time.Now().Format(time.RFC3339),
			"error":  "Database ping failed",
		})
		return
	}

	if _, err := os.Stat(STORAGE_PATH); os.IsNotExist(err) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unhealthy",
			"time":   time.Now().Format(time.RFC3339),
			"error":  "rClone mount not available",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "healthy",
		"time":     time.Now().Format(time.RFC3339),
		"database": "connected",
		"storage":  "available",
	})
}

func (s *AssetService) getSmartStoragePath(assetID string) string {
	hash := md5.Sum([]byte(assetID))
	hexHash := hex.EncodeToString(hash[:])

	parts := make([]string, 0, DIR_SHARDING_DEPTH)
	for i := 0; i < DIR_SHARDING_DEPTH && i*2 < len(hexHash); i++ {
		parts = append(parts, hexHash[i*2:i*2+2])
	}

	basePath := filepath.Join(parts...)
	s.mu.Lock()
	defer s.mu.Unlock()
	baseCounterVal, _ := s.directoryCounter.LoadOrStore(basePath, new(atomic.Int64))
	baseCounter := baseCounterVal.(*atomic.Int64)
	currentBaseCount := baseCounter.Load()

	if currentBaseCount >= FILES_PER_DIR {
		for i := range 100 {
			newPath := filepath.Join(basePath, fmt.Sprintf("bucket_%d", i))
			bucketCounterVal, _ := s.directoryCounter.LoadOrStore(newPath, new(atomic.Int64))
			bucketCounter := bucketCounterVal.(*atomic.Int64)
			currentBucketCount := bucketCounter.Load()

			if currentBucketCount < FILES_PER_DIR {
				bucketCounter.Add(1)
				return newPath
			}
		}
	}
	baseCounter.Add(1)
	return basePath
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

	assetID := xid.New().String()

	storagePath := assetService.getSmartStoragePath(assetID)

	fullDirPath := filepath.Join(STORAGE_PATH, storagePath)
	if err := os.MkdirAll(fullDirPath, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to create directory"))
		return
	}

	buffer := make([]byte, 512)
	n, err := c.Request.Body.Read(buffer)
	if err != nil && err != io.EOF {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to read request body"))
		return
	}

	mtype := mimetype.Detect(buffer[:n])
	destFileName := assetID + mtype.Extension()
	filePath := filepath.Join(fullDirPath, destFileName)

	out, err := os.Create(filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to create file"))
		return
	}
	defer out.Close()

	bodyReader := io.MultiReader(bytes.NewReader(buffer[:n]), c.Request.Body)

	hashWriter := md5.New()
	teeReader := io.TeeReader(bodyReader, hashWriter)

	size, err := io.Copy(out, teeReader)
	if err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to write file"))
		return
	}

	fileHash := hex.EncodeToString(hashWriter.Sum(nil)) // Get the MD5 hash

	asset := &Asset{
		ID:          assetID,
		StoragePath: storagePath,
		FileName:    destFileName,
		Size:        size,
		MimeType:    mtype.String(),
		Hash:        fileHash,
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
	filePath := filepath.Join(STORAGE_PATH, asset.StoragePath, assetName)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, ErrorResponse(404, "Asset file not found on storage"))
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%s", assetName))
	c.Header("Cache-Control", "max-age=2592000")

	c.File(filePath)
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

	filePath := filepath.Join(STORAGE_PATH, asset.StoragePath, asset.FileName)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to delete asset file"))
		return
	}

	if err := assetService.deleteAssetMetadata(assetID); err != nil {
		c.JSON(http.StatusInternalServerError, ErrorResponse(500, "Failed to delete asset metadata"))
		return
	}

	s.mu.Lock()
	dirPath := asset.StoragePath
	if actual, ok := assetService.directoryCounter.Load(dirPath); ok {
		if counter, ok := actual.(*atomic.Int64); ok {
			if counter.Load() > 0 {
				counter.Add(-1)
			}
		}
	}
	s.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
	})
}
