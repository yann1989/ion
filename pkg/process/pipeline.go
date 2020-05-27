package process

import (
	"sync"
	"time"

	"github.com/pion/ion/pkg/log"
	"github.com/pion/ion/pkg/process/elements"
	"github.com/pion/ion/pkg/process/samples"
	"github.com/pion/ion/pkg/rtc/transport"
)

const (
	liveCycle = 6 * time.Second
)

var (
	config Config
)

// Config for pipeline
type Config struct {
	SampleBuilder samples.BuilderConfig
	WebmSaver     elements.WebmSaverConfig
}

// Pipeline constructs a processing graph
//
//                                            +--->element
//                                            |
// pub--->pubCh-->sampleBuilder-->elementCh---+--->element
//                                            |
//                                            +--->element
type Pipeline struct {
	pub           transport.Transport
	elements      map[string]elements.Element
	elementLock   sync.RWMutex
	elementChans  map[string]chan *samples.Sample
	sampleBuilder *samples.Builder
	stop          bool
	liveTime      time.Time
}

// InitPipeline .
func InitPipeline(c Config) {
	config = c

	elements.InitWebmSaver(c.WebmSaver)
}

// NewPipeline return a new Pipeline
func NewPipeline(id string, pub transport.Transport) *Pipeline {
	log.Infof("NewPipeline id=%s", id)
	p := &Pipeline{
		pub:           pub,
		elements:      make(map[string]elements.Element),
		elementChans:  make(map[string]chan *samples.Sample),
		sampleBuilder: samples.NewBuilder(config.SampleBuilder),
		liveTime:      time.Now().Add(liveCycle),
	}

	if config.WebmSaver.DefaultOn {
		webm := elements.NewWebmSaver(id)
		p.AddElement(elements.TypeWebmSaver, webm)
	}

	p.start()

	return p
}

func (p *Pipeline) start() {
	go func() {
		for {
			if p.stop {
				return
			}

			pkt, err := p.pub.ReadRTP()
			if err != nil {
				log.Errorf("p.pub.ReadRTP err=%v", err)
				continue
			}
			p.liveTime = time.Now().Add(liveCycle)
			err = p.sampleBuilder.WriteRTP(pkt)
			if err != nil {
				log.Errorf("p.sampleBuilder.WriteRTP err=%v", err)
				continue
			}
		}
	}()

	go func() {
		for {
			if p.stop {
				return
			}

			sample := p.sampleBuilder.Read()

			p.elementLock.RLock()
			// Push to client send queues
			for _, element := range p.elements {
				err := element.Write(sample)
				if err != nil {
					log.Errorf("element.Write err=%v", err)
					continue
				}
			}
			p.elementLock.RUnlock()
		}
	}()
}

// AddElement add a element to pipeline
func (p *Pipeline) AddElement(name string, e elements.Element) {
	if p.elements[name] != nil {
		log.Errorf("Pipeline.AddElement element %s already exists.", name)
		return
	}
	p.elementLock.Lock()
	defer p.elementLock.Unlock()
	p.elements[name] = e
	p.elementChans[name] = make(chan *samples.Sample, 100)
	log.Infof("Pipeline.AddElement name=%s", name)
}

// GetElement get a node by id
func (p *Pipeline) GetElement(id string) elements.Element {
	p.elementLock.RLock()
	defer p.elementLock.RUnlock()
	return p.elements[id]
}

// DelElement del node by id
func (p *Pipeline) DelElement(id string) {
	log.Infof("Pipeline.DelElement id=%s", id)
	p.elementLock.Lock()
	defer p.elementLock.Unlock()
	if p.elements[id] != nil {
		p.elements[id].Close()
	}
	if p.elementChans[id] != nil {
		close(p.elementChans[id])
	}
	delete(p.elements, id)
	delete(p.elementChans, id)
}

func (p *Pipeline) delElements() {
	p.elementLock.RLock()
	keys := make([]string, 0, len(p.elements))
	for k := range p.elements {
		keys = append(keys, k)
	}
	p.elementLock.RUnlock()

	for _, id := range keys {
		p.DelElement(id)
	}
}

func (p *Pipeline) delPub() {
	if p.pub != nil {
		p.pub.Close()
	}
	p.sampleBuilder.Stop()
	p.pub = nil
}

// Close release all
func (p *Pipeline) Close() {
	if p.stop {
		return
	}
	log.Infof("Pipeline.Close")
	p.delPub()
	p.stop = true
	p.delElements()
}

// Alive return router status
func (p *Pipeline) Alive() bool {
	return p.liveTime.After(time.Now())
}