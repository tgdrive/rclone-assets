# Rclone Assets API

A high-performance, robust asset management microservice written in Go. It leverages **Rclone** for backend storage backend flexibility (S3, GCS, Local, etc.) and uses **PostgreSQL** for metadata management. It features a smart **Content Addressable Storage (CAS)** system with deduplication and a local **VFS Cache** for fast retrieval.

## Features

-   **Backend Agnostic:** Uses [Rclone](https://rclone.org/) to support over 70+ storage providers (AWS S3, Google Drive, Azure Blob, Local Filesystem, etc.).
-   **High Performance:** Built with [Gin](https://github.com/gin-gonic/gin) and [Gorm](https://gorm.io/).
-   **Smart Caching:** Implements Rclone's VFS (Virtual File System) with full caching support to minimize remote API calls and speed up reads.
-   **Deduplication:** Content Addressable Storage (CAS) based on MD5 hashes ensures identical files are only stored once, saving storage space.
-   **Structured Logging:** Integrated [Zap](https://github.com/uber-go/zap) logger for high-performance, structured logging.
-   **Secure:** API Key authentication using constant-time comparison to prevent timing attacks.
-   **Resilient:** Graceful shutdown and signal handling.

## Requirements

-   Go 1.24+
-   PostgreSQL Database
-   Rclone configured (or a valid remote string)

## Environment Variables

Configure the service using the following environment variables:

### Core Configuration
| Variable | Description | Required | Default |
|----------|-------------|:--------:|:-------:|
| `DATABASE_URL` | PostgreSQL connection string (DSN). | Yes | - |
| `REMOTE_PATH` | Rclone remote path (e.g., `s3:my-bucket/assets` or `/local/path`). | Yes | - |
| `API_KEY` | Secret key for authenticating API requests. | Yes | - |
| `PORT` | HTTP port to listen on. | No | `8080` |

### Caching & Performance
| Variable | Description | Default |
|----------|-------------|:-------:|
| `CACHE_DIR` | Local directory for VFS cache. | `/var/cache` |
| `DIR_CACHE_TIME` | How long to cache directory listings. | `60m` |
| `CACHE_MAX_AGE` | Max age of objects in the cache. | `24h` |
| `CACHE_MAX_SIZE` | Max total size of the local cache. | `10G` |

## API Endpoints

All endpoints (except public download) require the `X-API-Key` header.

### 1. Upload Asset
Upload a file. The system calculates the MD5 hash and deduplicates automatically.

-   **URL:** `/upload`
-   **Method:** `PUT`
-   **Headers:** `X-API-Key: <your-key>`
-   **Body:** Raw binary file content.

**Response:**
```json
{
  "success": true,
  "asset": {
    "id": "c123456789...",
    "fileName": "c123456789....jpg",
    "size": 1024,
    "mimeType": "image/jpeg",
    "hash": "d41d8cd98f00b204e9800998ecf8427e"
  },
  "deduped": false
}
```

### 2. List Assets
Get a paginated list of assets.

-   **URL:** `/assets`
-   **Method:** `GET`
-   **Headers:** `X-API-Key: <your-key>`
-   **Query Params:**
    -   `limit`: Number of items (default 100, max 1000).
    -   `offset`: Pagination offset (default 0).

### 3. Download Asset
Stream an asset directly from the cache/storage.

-   **URL:** `/assets/:name`
-   **Method:** `GET`
-   **Example:** `/assets/c123456789....jpg`
-   **Note:** The `:name` parameter must start with the Asset ID. The extension is optional but recommended for browsers.
-   **Auth:** Public (No API Key required by default, unless middleware is changed).

### 4. Delete Asset
Delete an asset's metadata.
*Note: Due to the CAS nature, the physical file is strictly deleted only if the hash is unique to this asset (logic implemented in code).*

-   **URL:** `/assets/:id`
-   **Method:** `DELETE`
-   **Headers:** `X-API-Key: <your-key>`

## Running the Project

### Local Development

1.  **Setup PostgreSQL:** Ensure you have a running database.
2.  **Run:**
    ```bash
    export DATABASE_URL="postgres://user:pass@localhost:5432/assets_db"
    export REMOTE_PATH="/tmp/assets-local-storage"
    export API_KEY="secret123"
    export CACHE_DIR="./tmp/cache"
    
    go run main.go
    ```

### Docker

(Assuming a Dockerfile exists or using the binary)

```bash
docker run -d \
  -e DATABASE_URL="postgres://..." \
  -e REMOTE_PATH="s3:my-bucket" \
  -e API_KEY="secret" \
  -p 8080:8080 \
  rclone-assets
```
