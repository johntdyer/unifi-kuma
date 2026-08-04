package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_IndependentRegistries(t *testing.T) {
	a := New("dev")
	b := New("dev")
	require.NotSame(t, a.Registry, b.Registry)

	// Both must be independently gatherable without panicking or colliding
	// (e.g. on the process/Go collectors, which use fixed metric names).
	_, err := a.Registry.Gather()
	require.NoError(t, err)
	_, err = b.Registry.Gather()
	require.NoError(t, err)
}

func TestRecordSync_Success(t *testing.T) {
	m := New("dev")
	m.RecordSync(2*time.Second, nil)

	assert.Equal(t, float64(1), testutil.ToFloat64(m.SyncsTotal.WithLabelValues("success")))
	assert.Equal(t, float64(0), testutil.ToFloat64(m.SyncsTotal.WithLabelValues("error")))
	assert.Positive(t, testutil.ToFloat64(m.LastSyncTimestamp))
	assert.Positive(t, testutil.ToFloat64(m.LastSuccessTimestamp))

	ok, detail := m.Healthy(0)
	assert.True(t, ok)
	assert.Equal(t, "ok", detail)
}

func TestRecordSync_Error(t *testing.T) {
	m := New("dev")
	m.RecordSync(time.Second, errors.New("boom"))

	assert.Equal(t, float64(1), testutil.ToFloat64(m.SyncsTotal.WithLabelValues("error")))
	assert.Positive(t, testutil.ToFloat64(m.LastSyncTimestamp))
	assert.Equal(t, float64(0), testutil.ToFloat64(m.LastSuccessTimestamp), "a failed sync must not update last-success")

	ok, detail := m.Healthy(0)
	assert.False(t, ok)
	assert.Contains(t, detail, "boom")
}

func TestHealthy_NoSyncYet(t *testing.T) {
	m := New("dev")
	ok, detail := m.Healthy(time.Hour)
	assert.False(t, ok)
	assert.Contains(t, detail, "no sync has completed")
}

func TestHealthy_StaleSuccess(t *testing.T) {
	m := New("dev")
	m.RecordSync(time.Second, nil)

	// Simulate time passing by asking for an impossible max age.
	ok, detail := m.Healthy(1 * time.Nanosecond)
	assert.False(t, ok)
	assert.Contains(t, detail, "exceeding")
}

func TestHealthy_MaxAgeZeroDisablesStaleness(t *testing.T) {
	m := New("dev")
	m.RecordSync(time.Second, nil)

	ok, _ := m.Healthy(0)
	assert.True(t, ok, "maxAge <= 0 should only check success, not recency")
}
