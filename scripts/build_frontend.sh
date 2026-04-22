#!/bin/bash

set -e

REPO_DIR="mytonprovider-org"
REPO_URL="https://github.com/dearjohndoe/mytonprovider-org.git"

mkdir -p /tmp/frontend
cd /tmp/frontend

if [ -d "$REPO_DIR" ]; then
    echo "Repository directory '$REPO_DIR' found. Pulling latest changes."
    cd "$REPO_DIR"
    git pull
else
    echo "Cloning repository from $REPO_URL."
    git clone "$REPO_URL"
    cd "$REPO_DIR"
fi


echo "Installing npm dependencies..."
npm install --legacy-peer-deps

echo "Building the project..."
if [ -n "$FRONTEND_API_BASE_URL" ]; then
    export NEXT_PUBLIC_API_BASE_URL="$FRONTEND_API_BASE_URL"
    echo "Using NEXT_PUBLIC_API_BASE_URL=$NEXT_PUBLIC_API_BASE_URL"
else
    echo "Using same-origin API URLs during build"
fi
npm run build

DOMAIN="${DOMAIN:-mytonprovider.org}"
# Use IP address if no domain is provided or if DOMAIN is an IP
if [[ "$DOMAIN" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    SITE_NAME="ip-${DOMAIN//./-}"
else
    SITE_NAME="$DOMAIN"
fi

WEB_DIR="/var/www/$SITE_NAME"
BUILD_DIR="out"

rm -rf "$WEB_DIR"
mkdir -p "$WEB_DIR"
cp -r "$BUILD_DIR"/* "$WEB_DIR/"

echo "Frontend deployment completed successfully."
