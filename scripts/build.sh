#!/bin/bash

set -e

IMAGE_NAME="discopanel"
IMAGE_TAG="dev"

# Panel image ships to ghcr, hub frozen at v2
IMAGES=(
    "ghcr.io/discohaus/${IMAGE_NAME}:${IMAGE_TAG}"
)

TAG_ARGS=()
for image in "${IMAGES[@]}"; do
    TAG_ARGS+=(-t "$image")
done

echo "Building ${IMAGES[*]}..."

docker build \
    "${TAG_ARGS[@]}" \
    -f docker/Dockerfile.discopanel \
    .

for image in "${IMAGES[@]}"; do
    echo "Pushing ${image}..."
    docker push "$image"
done

echo "Build and push complete: ${IMAGES[*]}"
