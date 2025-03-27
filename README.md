# Asset Storage API

A Go-based HTTP API service that provides secure asset management with filesystem storage and PostgreSQL metadata.

## Features

- Secure file upload/download API with API key authentication
- Automatic file organization with smart directory sharding (2 levels deep, 1000 files per directory)
- MD5 hashing for file integrity
- PostgreSQL backend for metadata storage
- CORS support for web applications
- Health check endpoint for monitoring
- Configurable upload size limits (default 50MB)

## Requirements

- Go 1.x
- PostgreSQL database
- Storage volume mounted at STORAGE_PATH

## Environment Variables

Required environment variables:

```env
DATABASE_URL="postgresql://user:password@localhost:5432/dbname"
STORAGE_PATH="/path/to/storage"
API_KEY="your-secure-api-key"
```

## API Endpoints

All API endpoints are mounted under `/api` prefix.

### Upload Asset
```http
PUT /api/upload
Header: X-API-Key: your-api-key
Content-Type: application/octet-stream

[binary data]
```

Returns:
```json
{
  "success": true,
  "message": "File uploaded successfully",
  "asset": {
    "id": "uuid",
    "name": "filename",
    "path": "relative/path",
    "size": 1234,
    "created_at": "2025-03-27T00:00:00Z",
    "updated_at": "2025-03-27T00:00:00Z",
    "mime_type": "application/octet-stream",
    "hash": "md5hash"
  }
}
```

### List Assets
```http
GET /api/assets
Header: X-API-Key: your-api-key
Query params: 
  - limit (default: 100, max: 1000)
  - offset (default: 0)
  - search (optional: filter assets by name)
```

### Get Asset Metadata
```http
GET /api/assets/:id
Header: X-API-Key: your-api-key
```

### Download Asset
```http
GET /api/download/:id
Header: X-API-Key: your-api-key
```

### Delete Asset
```http
DELETE /api/assets/:id
Header: X-API-Key: your-api-key
```

### Health Check
```http
GET /api/health
Header: X-API-Key: your-api-key
```


## Development

1. Clone the repository
2. Set up required environment variables
3. Run PostgreSQL database
4. Create storage directory and set permissions
5. Run the application:
```bash
go run main.go
```

The service will start on port 8080 by default.

## License

This project is licensed under the MIT License - see the LICENSE file for details.
