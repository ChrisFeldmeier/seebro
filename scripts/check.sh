#!/bin/bash
set -e

echo "🔍 Running pre-push checks (matches GitHub Actions CI)..."

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

check_step() {
    local step_name="$1"
    echo -e "\n${YELLOW}📋 $step_name${NC}"
}

success_step() {
    local step_name="$1"
    echo -e "${GREEN}✅ $step_name - PASSED${NC}"
}

error_step() {
    local step_name="$1"
    echo -e "${RED}❌ $step_name - FAILED${NC}"
    exit 1
}

# Step 1: Format check (exactly like CI)
check_step "Format Check"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
    echo -e "${RED}Files not formatted:${NC}"
    echo "$unformatted"
    echo -e "${YELLOW}Run: gofmt -w .${NC}"
    error_step "Format Check"
fi
success_step "Format Check"

# Step 2: Go vet
check_step "Go Vet"
if ! go vet ./...; then
    error_step "Go Vet"
fi
success_step "Go Vet"

# Step 3: Build
check_step "Build"
if ! go build -o seebro .; then
    error_step "Build"
fi
success_step "Build"

# Step 4: Tests with coverage
check_step "Tests with Coverage"
if ! go test ./... -v -count=1 -coverprofile=coverage.out -covermode=atomic; then
    error_step "Tests"
fi

# Show coverage summary (like CI)
echo -e "\n${YELLOW}📊 Coverage Summary:${NC}"
go tool cover -func=coverage.out | tail -1
success_step "Tests with Coverage"

# Step 5: Lint check (if golangci-lint is available)
check_step "Lint Check"
# Check common locations for golangci-lint
LINT_CMD=""
if command -v golangci-lint >/dev/null 2>&1; then
    LINT_CMD="golangci-lint"
elif [ -x "$HOME/bin/golangci-lint" ]; then
    LINT_CMD="$HOME/bin/golangci-lint"
elif [ -x "/usr/local/bin/golangci-lint" ]; then
    LINT_CMD="/usr/local/bin/golangci-lint"
fi

if [ -n "$LINT_CMD" ]; then
    if ! $LINT_CMD run ./...; then
        error_step "Lint Check"
    fi
    success_step "Lint Check"
else
    echo -e "${YELLOW}⚠️  golangci-lint not installed - skipping lint check${NC}"
    echo -e "${YELLOW}   Install with: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b ~/bin v2.9.0${NC}"
fi

# Step 6: Clean up
rm -f seebro coverage.out

echo -e "\n${GREEN}🎉 All checks passed! Ready to push.${NC}"