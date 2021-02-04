package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"time"
)

var (
	peeringProcessTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "liqo_peering_process_execution_time",
			Help:    "The elapsed time (ms) in processing of every liqo component involved in the peering process",
			Buckets: prometheus.LinearBuckets(100, 150, 20),
		},
		[]string{"liqo_component"},
	)

	peeringEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "liqo_peering_event",
			Help: "Main events occurring in liqo components during the peering process",
		},
		[]string{"liqo_component", "event", "status"})

	startProcessingTime time.Time

	// this map prevents a component to expose a metric related to the "begin" of an event more than ones, unless the related "end" event has been exposed
	// start events are usually called within Reconcile functions, this map prevents to have multiple start event and only one end event
	consistencyStartEventMap = createConsistencyEventMap()

	// end events are usually called in "safer" places, preventing multiple calls to end events
	// TODO: consider to implement a similar behaviour also for end events
)

func init() {

	go func() {
		// Register custom metrics with the global prometheus registry
		prometheus.MustRegister(peeringProcessTime)
		prometheus.MustRegister(peeringEvents)

		http.Handle("/metrics", promhttp.HandlerFor(
			prometheus.DefaultGatherer,
			promhttp.HandlerOpts{
				// Opt into OpenMetrics to support exemplars.
				EnableOpenMetrics: true,
			},
		))

		err := http.ListenAndServe(":8090", nil)
		if err != nil {
			panic("ListenAndServe:" + err.Error())
		}
	}()
}

func PeeringProcessExecutionStarted() {
	startProcessingTime = time.Now()
}

func PeeringProcessExecutionCompleted(component LiqoComponent) {
	processingTimeMS := (time.Now().UnixNano() - startProcessingTime.UnixNano()) / 1000000
	peeringProcessTime.WithLabelValues(component.String()).Observe(float64(processingTimeMS))
}

func PeeringProcessEventRegister(component LiqoComponent, event EventType, status EventStatus) {
	if status == End {
		peeringEvents.WithLabelValues(component.String(), event.String(), status.String()).Inc()

		mapKey := component.String() + event.String()
		consistencyStartEventMap[mapKey] = true
	} else {
		mapKey := component.String() + event.String()
		if consistencyStartEventMap[mapKey] {
			peeringEvents.WithLabelValues(component.String(), event.String(), status.String()).Inc()
			consistencyStartEventMap[mapKey] = false
		}
	}
}

func createConsistencyEventMap() map[string]bool {
	retMap := make(map[string]bool)

	for i := LiqoComponent(0); i < lastComponent; i++ {
		for j := EventType(0); j < lastEvent; j++ {
			retMap[i.String()+j.String()] = true
		}
	}

	return retMap
}
