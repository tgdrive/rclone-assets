#!/bin/sh
set -e

# Default cache size
RCLONE_CACHE_SIZE=${RCLONE_CACHE_SIZE:-"10G"}
RCLONE_CONFIG_FILE=${RCLONE_CONFIG_FILE:-"/app/rclone/rclone.conf"}
RCLONE_CACHE_DIR=${RCLONE_CACHE_DIR:-"/var/rclone-cache"}
RCLONE_CACHE_MAX_AGE=${RCLONE_CACHE_MAX_AGE:-"768h"}
RCLONE_CACHE_MODE=${RCLONE_CACHE_MODE:-"full"}
DIR_CACHE_TIME=${DIR_CACHE_TIME:-"8670h"}
POLL_INTERVAL=${POLL_INTERVAL-"5m"}

# Ensure cache directory exists
mkdir -p "$RCLONE_CACHE_DIR"

# Check if we need to mount rclone remote
if [ -n "$RCLONE_REMOTE" ] && [ -n "$RCLONE_REMOTE_PATH" ]; then
    echo "Mounting rclone remote: $RCLONE_REMOTE:$RCLONE_REMOTE_PATH to /app/rclone-assets"
    
    # Configure full caching mode for optimal performance
    RCLONE_MOUNT_OPTS="
        --vfs-cache-mode $RCLONE_CACHE_MODE
        --vfs-cache-max-size $RCLONE_CACHE_SIZE
        --vfs-cache-max-age $RCLONE_CACHE_MAX_AGE
        --dir-cache-time $DIR_CACHE_TIME
        --poll-interval $POLL_INTERVAL
        --cache-dir $RCLONE_CACHE_DIR
        --config $RCLONE_CONFIG_FILE
        --allow-other
        --allow-non-empty
        --umask 0000
        $RCLONE_EXTRA_OPTS
    "
    
    # Remove newlines
    MOUNT_OPTS=$(echo "$RCLONE_MOUNT_OPTS" | tr '\n' ' ')

    # Mount the remote in background
    rclone mount "$RCLONE_REMOTE:$RCLONE_REMOTE_PATH" /app/rclone-assets $MOUNT_OPTS &
    RCLONE_PID=$!
    
    # Wait for mount to be available
    echo "Waiting for rclone mount to be ready..."
    timeout=60  # Increased timeout for large remotes
    while [ $timeout -gt 0 ]; do
        if mountpoint -q /app/rclone-assets; then
            echo "rClone mount is ready."
            break
        fi
        sleep 1
        timeout=$((timeout - 1))
        # Check if rclone process is still running
        if ! kill -0 $RCLONE_PID 2>/dev/null; then
            echo "ERROR: rClone mount process died. Check logs."
            exit 1
        fi
    done
    
    if [ $timeout -eq 0 ]; then
        echo "ERROR: rClone mount timed out. Check your configuration."
        exit 1
    fi
fi

# Set environment variables from .env if they aren't already set
[ -z "$API_KEY" ] && [ -f ".env" ] && export $(grep -v '^#' .env | xargs)

# Change working directory
cd /app

# Set permissions for mounted directory
chmod -R 755 /app/rclone-assets

# If running as root, switch to appuser
if [ "$(id -u)" = "0" ]; then
    chown -R appuser:appgroup /app/rclone-assets $RCLONE_CACHE_DIR
    exec su-exec appuser ./rclone-api
else
    # Already running as non-root
    exec ./rclone-api
fi

# Add trap for cleanup (This will execute if the script is terminated by a signal)
trap 'echo "Caught signal, unmounting rclone"; fusermount -u /app/rclone-assets; exit 1' TERM INT QUIT
