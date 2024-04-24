VERSION ?= 0.9.1-sabre-1

DOCKER_IMAGE_NAME ?= sabre/gh/arc/actions-runner-controller
DOCKER_IMAGE_VERSION ?= 0.9.1-sabre-1

COMMIT_SHA = $(shell git rev-parse HEAD)

ifeq (${PLATFORMS}, )
	export PLATFORMS="linux/amd64"
endif

package-controller-chart:  ## build controller chart
	@echo "Building package-controller"
	@cd charts/gha-runner-scale-set-controller && helm package . --version ${VERSION} --app-version ${VERSION}

package-runnerset-chart: ## build runnerset chart
	@echo "Building package-runnerset"
	@cd charts/gha-runner-scale-set && helm package . --version ${VERSION} --app-version ${VERSION}

upload-charts: package-controller-chart package-runnerset-chart ## upload charts
	@echo "Uploading charts"
	@ngp nexus raw upload charts/gha-runner-scale-set-controller/gha-runner-scale-set-controller-${VERSION}.tgz sabre/gh/forked-arc/charts/gha-runner-scale-set-controller-${VERSION}.tgz
	@ngp nexus raw upload charts/gha-runner-scale-set/gha-runner-scale-set-${VERSION}.tgz sabre/gh/forked-arc/charts/gha-runner-scale-set-${VERSION}.tgz

build-controller-image: ## build docker image, it will not be pushed to remote repository
	@echo "Building image"
	export DOCKER_CLI_EXPERIMENTAL=enabled ;\
	export DOCKER_BUILDKIT=1
	export PUSH_ARG="--load"

	@if ! docker buildx ls | grep -q container-builder; then\
		docker buildx create --platform ${PLATFORMS} --name container-builder --use;\
	fi
	docker buildx build \
		--platform ${PLATFORMS}	\
        --build-arg VERSION=${VERSION} \
        --build-arg COMMIT_SHA=${COMMIT_SHA} \
        -t "${DOCKER_IMAGE_NAME}:${DOCKER_IMAGE_VERSION}" \
        -f Dockerfile \
        . --load

upload-image: build-controller-image ## build and push docker image to repository
	@echo "Uploading image"
	@ngp nexus docker upload "${DOCKER_IMAGE_NAME}:${DOCKER_IMAGE_VERSION}" gh/forked-arc/actions-runner-controller:${DOCKER_IMAGE_VERSION}

upload-all: upload-charts upload-image ## upload all built charts and defaults to raw-staging repo
	@echo "All charts and defaults uploaded to raw-staging"

help: ## show usage and tasks (default)
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
