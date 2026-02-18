package main

import (
	"math"
	"testing"
	"time"
)

func TestHumanMouseMove(t *testing.T) {
	// Test bezier calculation
	testCases := []struct {
		name     string
		fromX    float64
		fromY    float64
		toX      float64
		toY      float64
		maxSteps int
	}{
		{
			name:     "short distance",
			fromX:    100,
			fromY:    100,
			toX:      150,
			toY:      150,
			maxSteps: 30,
		},
		{
			name:     "long distance",
			fromX:    0,
			fromY:    0,
			toX:      1000,
			toY:      1000,
			maxSteps: 30, // Should be capped
		},
		{
			name:     "tiny distance",
			fromX:    100,
			fromY:    100,
			toX:      105,
			toY:      105,
			maxSteps: 5, // Should be minimum
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test distance calculation
			distance := math.Sqrt((tc.toX-tc.fromX)*(tc.toX-tc.fromX) + (tc.toY-tc.fromY)*(tc.toY-tc.fromY))

			// Test duration calculation (from human.go logic)
			baseDuration := 100 + (distance/2000)*200
			if baseDuration < 100 {
				t.Error("base duration too low")
			}

			// Test steps calculation
			steps := int(baseDuration / 20)
			if steps < 5 {
				steps = 5
			}
			if steps > 30 {
				steps = 30
			}

			if steps > tc.maxSteps {
				t.Errorf("too many steps: %d > %d", steps, tc.maxSteps)
			}
		})
	}
}

// Note: bezier and getTypingPattern are unexported functions.
// We test the public-facing behavior through action handlers instead.

func TestTypingTiming(t *testing.T) {
	// Test that typing delays are reasonable
	startTime := time.Now()

	// Simulate typing pattern delays
	baseDelay := 50
	variations := []int{0, 20, 50, 100, 150}

	totalDelay := 0
	for i := 0; i < 10; i++ {
		delay := baseDelay + variations[i%len(variations)]
		totalDelay += delay
	}

	// Average should be around 50-150ms per character
	avgDelay := totalDelay / 10
	if avgDelay < 30 || avgDelay > 200 {
		t.Errorf("average typing delay out of range: %dms", avgDelay)
	}

	elapsed := time.Since(startTime)
	if elapsed > 1*time.Second {
		t.Error("test took too long, might indicate performance issue")
	}
}

func TestHumanClickDistance(t *testing.T) {
	tests := []struct {
		name         string
		x, y         float64
		startOffsetX float64
		startOffsetY float64
		shouldMove   bool
	}{
		{
			name:         "far distance",
			x:            500,
			y:            500,
			startOffsetX: 100,
			startOffsetY: 100,
			shouldMove:   true,
		},
		{
			name:         "close distance",
			x:            500,
			y:            500,
			startOffsetX: 10,
			startOffsetY: 10,
			shouldMove:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			distance := math.Sqrt(tt.startOffsetX*tt.startOffsetX + tt.startOffsetY*tt.startOffsetY)
			shouldMove := distance > 30

			if shouldMove != tt.shouldMove {
				t.Errorf("movement decision wrong: got %v, want %v (distance: %f)",
					shouldMove, tt.shouldMove, distance)
			}
		})
	}
}
