package main

import (
	"time"
)

// getLocalTime returns the current time adjusted by the configured offset
func (m *Monitor) getLocalTime() time.Time {
	return time.Now().Add(time.Duration(m.config.TimeOffsetHours) * time.Hour)
}

// formatLocalTime formats a time with the configured offset
func (m *Monitor) formatLocalTime(t time.Time) string {
	localTime := t.Add(time.Duration(m.config.TimeOffsetHours) * time.Hour)
	return localTime.Format("2006-01-02 15:04:05")
}

// formatLocalTimeShort formats a time with the configured offset (short format)
func (m *Monitor) formatLocalTimeShort(t time.Time) string {
	localTime := t.Add(time.Duration(m.config.TimeOffsetHours) * time.Hour)
	return localTime.Format("15:04:05")
}

