package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	kitlogrus "github.com/go-kit/kit/log/logrus"
	discardMetrics "github.com/go-kit/kit/metrics/discard"
	expvarMetrics "github.com/go-kit/kit/metrics/expvar"
	kitinflux "github.com/go-kit/kit/metrics/influx"
	prometheusMetrics "github.com/go-kit/kit/metrics/prometheus"
	"github.com/gorilla/mux"
	influx "github.com/influxdata/influxdb1-client/v2"
	"github.com/itzg/mc-router/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

type MetricsBuilder interface {
	BuildConnectorMetrics() *server.ConnectorMetrics
	Start(ctx context.Context) error
}

func NewMetricsBuilder(backend string, config *MetricsBackendConfig) MetricsBuilder {
	switch strings.ToLower(backend) {
	case "expvar":
		return &expvarMetricsBuilder{}
	case "influxdb":
		return &influxMetricsBuilder{config: config}
	case "prometheus":
		return &prometheusMetricsBuilder{config: config}
	default:
		return &discardMetricsBuilder{}
	}
}

type expvarMetricsBuilder struct {
}

func (b expvarMetricsBuilder) Start(ctx context.Context) error {
	// nothing needed
	return nil
}

func (b expvarMetricsBuilder) BuildConnectorMetrics() *server.ConnectorMetrics {
	return &server.ConnectorMetrics{
		Errors:            expvarMetrics.NewCounter("errors").With("subsystem", "connector"),
		BytesTransmitted:  expvarMetrics.NewCounter("bytes"),
		Connections:       expvarMetrics.NewCounter("connections"),
		ActiveConnections: expvarMetrics.NewGauge("active_connections"),
	}
}

type discardMetricsBuilder struct {
}

func (b discardMetricsBuilder) Start(ctx context.Context) error {
	// nothing needed
	return nil
}

func (b discardMetricsBuilder) BuildConnectorMetrics() *server.ConnectorMetrics {
	return &server.ConnectorMetrics{
		Errors:            discardMetrics.NewCounter(),
		BytesTransmitted:  discardMetrics.NewCounter(),
		Connections:       discardMetrics.NewCounter(),
		ActiveConnections: discardMetrics.NewGauge(),
	}
}

type influxMetricsBuilder struct {
	config  *MetricsBackendConfig
	metrics *kitinflux.Influx
}

func (b *influxMetricsBuilder) Start(ctx context.Context) error {
	influxConfig := &b.config.Influxdb
	if influxConfig.Addr == "" {
		return errors.New("influx addr is required")
	}

	ticker := time.NewTicker(influxConfig.Interval)
	client, err := influx.NewHTTPClient(influx.HTTPConfig{
		Addr:     influxConfig.Addr,
		Username: influxConfig.Username,
		Password: influxConfig.Password,
	})
	if err != nil {
		return fmt.Errorf("failed to create influx http client: %w", err)
	}

	go b.metrics.WriteLoop(ctx, ticker.C, client)

	logrus.WithField("addr", influxConfig.Addr).
		Debug("reporting metrics to influxdb")

	return nil
}

func (b *influxMetricsBuilder) BuildConnectorMetrics() *server.ConnectorMetrics {
	influxConfig := &b.config.Influxdb

	metrics := kitinflux.New(influxConfig.Tags, influx.BatchPointsConfig{
		Database:        influxConfig.Database,
		RetentionPolicy: influxConfig.RetentionPolicy,
	}, kitlogrus.NewLogger(logrus.StandardLogger()))

	b.metrics = metrics

	return &server.ConnectorMetrics{
		Errors:            metrics.NewCounter("mc_router_errors"),
		BytesTransmitted:  metrics.NewCounter("mc_router_transmitted_bytes"),
		Connections:       metrics.NewCounter("mc_router_connections"),
		ActiveConnections: metrics.NewGauge("mc_router_connections_active"),
	}
}

type prometheusMetricsBuilder struct {
	config   *MetricsBackendConfig
	registry *prometheus.Registry
}

func (b prometheusMetricsBuilder) Start(ctx context.Context) error {
	prometheusConfig := &b.config.Prometheus

	go func() {
		routes := mux.NewRouter()
		routes.Handle("/metrics", promhttp.HandlerFor(b.registry, promhttp.HandlerOpts{}))
		logrus.WithError(http.ListenAndServe(prometheusConfig.Bind, routes)).
			Error("Prometheus server failed")
		logrus.WithField("addr", prometheusConfig.Bind).
			Info("Prometheus metrics server has stopped")
	}()
	logrus.WithField("addr", prometheusConfig.Bind).
		Info("Prometheus metrics server has started")
	return nil
}

func (b *prometheusMetricsBuilder) BuildConnectorMetrics() *server.ConnectorMetrics {
	metricErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mc_router",
		Name:      "errors",
	}, []string{"type", "subsystem"})
	metricBytes := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mc_router",
		Name:      "transmitted_bytes",
	}, []string{})
	metricConnections := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "mc_router",
		Name:      "connections",
	}, []string{"side", "host"})
	metricActiveConnections := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "mc_router",
		Name:      "active_connections",
	}, []string{})

	registry := prometheus.NewRegistry()

	registry.MustRegister(metricErrors)
	registry.MustRegister(metricBytes)
	registry.MustRegister(metricConnections)
	registry.MustRegister(metricActiveConnections)

	b.registry = registry

	return &server.ConnectorMetrics{
		Errors:            prometheusMetrics.NewCounter(metricErrors).With("subsystem", "connector"),
		BytesTransmitted:  prometheusMetrics.NewCounter(metricBytes),
		Connections:       prometheusMetrics.NewCounter(metricConnections).With("host", ""),
		ActiveConnections: prometheusMetrics.NewGauge(metricActiveConnections),
	}
}
