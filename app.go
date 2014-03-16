package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/ant0ine/go-json-rest"
	"github.com/davecheney/gpio"
	"github.com/davecheney/gpio/rpi"
	"github.com/steveyen/gkvlite"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sethwklein.net/go/errutil"
	"time"
)

type User struct {
	Id       int
	Name     string
	Password string
}

type Event struct {
	Time     uint64
	Name     string
	Action   string
	WasArmed bool
}

type Flag struct {
	Name  string
	Value int
}

type Sensor struct {
	Name string
	Pin  int
}

type SystemStatus struct {
	Status     string
	LastUpdate time.Time
}

var f os.File
var sensors []Sensor
var s gkvlite.Store
var sysStatus SystemStatus
var dbname = "/tmp/test.db"

func setObj(collection string, key []byte, object interface{}) (err error) {
	f2, er := os.OpenFile(dbname, os.O_RDWR, 0666)
	defer errutil.AppendCall(&err, f2.Close)
	if er != nil {
		log.Println("Error while opening db", er)
		return er
	}
	s2, er := gkvlite.NewStore(f2)
	if er != nil {
		log.Println("Error while opening store", er)
		return er
	}
	defer errutil.AppendCall(&err, s2.Flush)

	c := s2.SetCollection(collection, nil)

	objectBytes, er := json.Marshal(object)
	if er != nil {
		log.Println("Error while marshalling object in collection", collection, object, er)
		return er
	}

	er = c.Set(key, objectBytes)
	if er != nil {
		log.Println("Error while saving object", key, collection, object, er)
		return er
	}
	// log.Println("Set", collection, key, object)
	if er := s2.Flush(); er != nil {
		log.Println("Error flushing changes to disk")
		return er
	}
	f2.Sync()
	return err
}

func AddEvent(e Event) error {
	if e.Time == 0 {
		e.Time = uint64(time.Now().UnixNano())
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, e.Time)
	return setObj("events", b, e)
}

func GetAllEvents() (err error, events []Event) {
	log.Println("getting all events")
	f2, er := os.Open(dbname)
	defer errutil.AppendCall(&err, f2.Close)
	if er != nil || f2 == nil {
		log.Println("eror while opening db", er)
		return er, events
	}
	s2, er := gkvlite.NewStore(f2)
	if er != nil {
		log.Println("eror while opening store", er)
		return er, events
	}

	c := s2.GetCollection("events")
	maxItem, er := c.MaxItem(true)
	if er != nil {
		return er, events
	}
	if maxItem == nil {
		return nil, events
	}
	j := Event{}
	er = json.Unmarshal(maxItem.Val, &j)
	events = append(events, j)
	log.Println("Here", j)
	er = c.VisitItemsDescend(maxItem.Key, true, func(i *gkvlite.Item) bool {
		j := Event{}
		er := json.Unmarshal(i.Val, &j)
		if er != nil {
			log.Println("eror unmarshalling value", er)
		} else {
			events = append(events, j)
		}
		log.Println("Here", j)
		return true
	})

	if er != nil {
		log.Println("eror scanning table 'events'", er)
		return er, events
	}

	return err, events
}

func ArmSystem() error {
	e := Event{}
	e.Action = "arm"
	e.Name = "system"
	e.WasArmed = false
	err := AddEvent(e)
	if err != nil {
		return err
	}
	return setObj("flags", []byte("armed"), 1)
}

func DisarmSystem() error {
	e := Event{}
	e.Action = "disarm"
	e.Name = "system"
	e.WasArmed = true
	err := AddEvent(e)
	if err != nil {
		return err
	}
	return setObj("flags", []byte("armed"), 0)
}

func HandleGetAllEvents(w *rest.ResponseWriter, req *rest.Request) {

	_, data := GetAllEvents()
	w.WriteJson(data)

}

func handleGetStatus(w *rest.ResponseWriter, req *rest.Request) {
	w.WriteJson(sysStatus)
}

func GetUser(w *rest.ResponseWriter, req *rest.Request) {
	f2, er := os.Open(dbname)
	if er != nil {
		log.Fatal("Error opening database", er)
		return
	}

	defer f2.Close()

	s2, er := gkvlite.NewStore(f2)
	if er != nil {
		log.Fatal("Error opening store", er)
		return
	}
	c := s2.GetCollection("users")

	var user User

	userBytes, er := c.Get([]byte(req.PathParam("id")))
	if er != nil {
		log.Fatal(er)
		return
	}
	if userBytes == nil {
		rest.NotFound(w, req)
		return
	}

	er = json.Unmarshal(userBytes, &user)
	if er != nil {
		log.Fatal(er)
		return
	}

	w.WriteJson(&user)
	return

}

func updateSystemStatus(newStatus string) {
	sysStatus = SystemStatus{
		Status:     newStatus,
		LastUpdate: time.Now(),
	}
}

func watchSensor(sensor Sensor) {
	pin, err := rpi.OpenPin(sensor.Pin, gpio.ModeInput)
	fmt.Printf("opening pin for %s (%d)!\n", sensor.Name, sensor.Pin)
	if err != nil {
		fmt.Printf("Error opening pin for %s (%d)! %s\n", sensor.Name, sensor.Pin, err)
		return
	}

	err = pin.BeginWatch(gpio.EdgeBoth, func() {
		if pin.Get() {
			fmt.Printf("OPEN for %s (%d) triggered!\n\n", sensor.Name, sensor.Pin)
		} else {
			fmt.Printf("CLOSE for %s (%d) triggered!\n\n", sensor.Name, sensor.Pin)
		}
	})
	if err != nil {
		fmt.Printf("Unable to watch pin: %s\n", err.Error())
		os.Exit(1)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			fmt.Println("Closing pin and terminating program.")
			pin.Close()
		}
	}()
}

func setupGPIO() {

	for _, sensor := range sensors {
		go watchSensor(sensor)
	}

	//blink
	power, err := gpio.OpenPin(rpi.GPIO17, gpio.ModeOutput)
	if err != nil {
		fmt.Printf("Error opening pin! %s\n", err)
		return
	}

	// clean up on exit
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			fmt.Println("Closing blink and terminating program.")
			power.Clear()
			power.Close()
			os.Exit(0)
		}
	}()

	fmt.Println("Now watching pin 22 on a falling edge.")
	updateSystemStatus("Ready")

	for {
		// log.Println("Setting power high")
		power.Set()
		time.Sleep(2000 * time.Millisecond)
		// log.Println("Setting power low")
		power.Clear()
		time.Sleep(2000 * time.Millisecond)
	}
}

func main() {
	updateSystemStatus("Unknown")

	sensors = append(sensors, Sensor{Name: "Front Door", Pin: rpi.GPIO22})

	setupGPIO()
	name := dbname
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0660)
	if err != nil {
		if os.IsExist(err) {
			log.Println("Using existing database")
		} else {
			log.Fatal(err)
		}
	}
	s, err := gkvlite.NewStore(f)

	s.Flush()
	f.Close()

	e := Event{}
	e.Name = "front door"
	e.Action = "opened"
	e.WasArmed = true

	//add some sample events
	ArmSystem()
	AddEvent(e)
	AddEvent(e)
	AddEvent(e)
	AddEvent(e)
	AddEvent(e)
	DisarmSystem()

	handler := rest.ResourceHandler{}
	handler.DisableJsonIndent = true
	handler.SetRoutes(
		rest.Route{"GET", "/users/:id", GetUser},
		rest.Route{"GET", "/events", HandleGetAllEvents},
	)
	http.ListenAndServe(":8081", &handler)
}
