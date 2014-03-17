package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/ant0ine/go-json-rest"
	"github.com/davecheney/gpio"
	"github.com/davecheney/gpio/rpi"
	"github.com/hoisie/mustache"
	"github.com/jimlawless/cfg"
	"github.com/steveyen/gkvlite"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"os/signal"
	"path"
	"reflect"
	"sethwklein.net/go/errutil"
	"strconv"
	"strings"
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
	Time  time.Time
}

type Sensor struct {
	Name  string
	Pin   int
	Alarm bool
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
var config map[string]string

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
		log.Println("error while opening db", er)
		return er, events
	}
	s2, er := gkvlite.NewStore(f2)
	if er != nil {
		log.Println("error while opening store", er)
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
			log.Println("error unmarshalling value", er)
		} else {
			events = append(events, j)
		}
		log.Println("Here", j)
		return true
	})

	if er != nil {
		log.Println("error scanning table 'events'", er)
		return er, events
	}

	return err, events
}

func GetFlag(flagName string) (err error, flag Flag) {
	f2, er := os.Open(dbname)
	defer errutil.AppendCall(&err, f2.Close)
	if er != nil || f2 == nil {
		log.Println("error while opening db", er)
		return er, flag
	}
	s2, er := gkvlite.NewStore(f2)
	if er != nil {
		log.Println("error while opening store", er)
		return er, flag
	}

	c := s2.GetCollection("flags")
	i, er := c.Get([]byte(flagName))
	if er != nil {
		log.Println("Error getting flag", err)
		return er, flag
	}

	er = json.Unmarshal(i, &flag)
	if er != nil {
		log.Println("Error unmarshalling value", er)
		return er, flag
	}

	return nil, flag
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
	updateSystemStatus("Armed")
	return setObj("flags", []byte("armed"), Flag{Name: "armed", Value: 1, Time: time.Now()})
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
	updateSystemStatus("Disarmed")
	return setObj("flags", []byte("armed"), Flag{Name: "armed", Value: 0, Time: time.Now()})
}

func HandleGetAllEvents(w *rest.ResponseWriter, req *rest.Request) {

	_, data := GetAllEvents()
	w.WriteJson(data)

}

func handleGetStatus(w *rest.ResponseWriter, req *rest.Request) {
	w.WriteJson(sysStatus)
}

func wantsHtml(req *rest.Request) bool {
	isHtml := false
	accepts := strings.Split(req.Request.Header["Accept"][0], ",")
	for _, b := range accepts {
		log.Println(b)
		if b == "text/html" {
			isHtml = true
			break
		} else if b == "application/hal+json" || b == "application/json" {
			isHtml = false
			break
		}
	}
	return isHtml
}

func parseDateToReadable(t time.Time) string {
	layout := "Mon, 01/02/06, 03:04PM"
	edt, _ := time.LoadLocation("America/New_York")
	return t.In(edt).Format(layout)
}

func getSystemStatus(w *rest.ResponseWriter, req *rest.Request) {
	if wantsHtml(req) {
		filename := path.Join(path.Join(os.Getenv("PWD"), "templates"), "status.mustache")
		output := mustache.RenderFile(filename, map[string]interface{}{"system_status": map[string]string{"Status": sysStatus.Status, "LastUpdateStr": parseDateToReadable(sysStatus.LastUpdate)}, "sensors": sensors})
		w.ResponseWriter.Write([]byte(output))

	} else {
		w.WriteJson(sensors)
	}
	return
}

func updateSystemStatus(newStatus string) {
	sysStatus = SystemStatus{
		Status:     newStatus,
		LastUpdate: time.Now(),
	}
}

func handleStatic(w *rest.ResponseWriter, r *rest.Request) {
	http.ServeFile(w.ResponseWriter, r.Request, r.Request.URL.Path[1:])
}
func handleVendor(w *rest.ResponseWriter, r *rest.Request) {
	http.ServeFile(w.ResponseWriter, r.Request, r.Request.URL.Path[1:])
}
func handleArmSystem(w *rest.ResponseWriter, r *rest.Request) {
	ArmSystem()
	http.Redirect(w.ResponseWriter, r.Request, "/status", 302)
}
func handleDisarmSystem(w *rest.ResponseWriter, r *rest.Request) {
	DisarmSystem()
	http.Redirect(w.ResponseWriter, r.Request, "/status", 302)
}

func handleDefault(w *rest.ResponseWriter, r *rest.Request) {
	http.Redirect(w.ResponseWriter, r.Request, "http://foo", 302)
}

func notifyAlert(sensor *Sensor) {
	// Set up authentication information.
	auth := smtp.PlainAuth(
		"",
		config["smtp_user"],
		config["smtp_pass"],
		config["smtp_host"],
	)
	// Connect to the server, authenticate, set the sender and recipient,
	// and send the email all in one step.
	status := "OPEN"
	if !sensor.Alarm {
		status = "CLOSED"
	}
	err := smtp.SendMail(
		config["smtp_host"]+":"+config["smtp_port"],
		auth,
		"chad.kouse@gmail.com",
		[]string{config["notify"]},
		[]byte(sensor.Name+" is now "+status),
	)
	if err != nil {
		log.Fatal(err)
	}
}

func watchSensor(sensor *Sensor) {
	pin, err := rpi.OpenPin(sensor.Pin, gpio.ModeInput)
	fmt.Printf("opening pin for %s (%d)!\n", sensor.Name, sensor.Pin)
	if err != nil {
		fmt.Printf("Error opening pin for %s (%d)! %s\n", sensor.Name, sensor.Pin, err)
		return
	}

	pin.Set()

	sensor.Alarm = pin.Get()
	fmt.Printf("%s currently open? %t\n", sensor.Name, sensor.Alarm)

	err = pin.BeginWatch(gpio.EdgeBoth, func() {
		if pin.Get() && !sensor.Alarm {
			sensor.Alarm = pin.Get()
			fmt.Printf("OPEN for %s (%d) triggered!\n\n", sensor.Name, sensor.Pin)
			if sysStatus.Status == "Armed" {
				//notify
				notifyAlert(sensor)
			}
		} else if !pin.Get() && sensor.Alarm {
			sensor.Alarm = pin.Get()
			fmt.Printf("CLOSE for %s (%d) triggered!\n\n", sensor.Name, sensor.Pin)
			if sysStatus.Status == "Armed" {
				//notify
				notifyAlert(sensor)
			}
		}
		sensor.Alarm = pin.Get()
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

	for i := range sensors {
		go watchSensor(&sensors[i])
	}

	//blink
	// power, err := gpio.OpenPin(rpi.GPIO17, gpio.ModeOutput)
	// if err != nil {
	// 	fmt.Printf("Error opening pin! %s\n", err)
	// 	return
	// }

	// clean up on exit
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			fmt.Println("Closing blink and terminating program.")
			// power.Clear()
			// power.Close()
			os.Exit(0)
		}
	}()

	updateSystemStatus("Ready")

	// for {
	// 	// log.Println("Setting power high")
	// 	power.Set()
	// 	time.Sleep(2000 * time.Millisecond)
	// 	// log.Println("Setting power low")
	// 	power.Clear()
	// 	time.Sleep(2000 * time.Millisecond)
	// }
}

func structToMap(i interface{}) map[string]string {
	values := make(map[string]string)
	iVal := reflect.ValueOf(i).Elem()
	typ := iVal.Type()
	for i := 0; i < iVal.NumField(); i++ {
		f := iVal.Field(i)
		// You ca use tags here...
		// tag := typ.Field(i).Tag.Get("tagname")
		// Convert each type into a string for the url.Values string map
		var v string
		switch f.Interface().(type) {
		case int, int8, int16, int32, int64:
			v = strconv.FormatInt(f.Int(), 10)
		case uint, uint8, uint16, uint32, uint64:
			v = strconv.FormatUint(f.Uint(), 10)
		case float32:
			v = strconv.FormatFloat(f.Float(), 'f', 4, 32)
		case float64:
			v = strconv.FormatFloat(f.Float(), 'f', 4, 64)
		case []byte:
			v = string(f.Bytes())
		case string:
			v = f.String()
		}
		values[typ.Field(i).Name] = v
	}
	return values
}

func main() {

	config = make(map[string]string)
	err := cfg.Load("config.cfg", config)
	if err != nil {
		log.Fatal(err)
	}

	updateSystemStatus("Unknown")

	sensors = append(sensors, Sensor{Name: "Front Door", Pin: rpi.GPIO4})
	sensors = append(sensors, Sensor{Name: "Deck Door", Pin: rpi.GPIO17})
	sensors = append(sensors, Sensor{Name: "Laundry Door", Pin: rpi.GPIO27})
	fmt.Println(sensors)

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

	err, flag := GetFlag("armed")
	if err != nil {
		log.Println("Error getting initial armed flag.  Assuming armed")
		flag = Flag{Name: "armed", Value: 1, Time: time.Now()}
		ArmSystem()
	}

	sysStatus.LastUpdate = flag.Time
	if flag.Value == 1 {
		sysStatus.Status = "Armed"
	} else {
		sysStatus.Status = "Disarmed"
	}

	handler := rest.ResourceHandler{}
	handler.DisableJsonIndent = true
	handler.SetRoutes(
		rest.Route{"GET", "/", handleDefault},
		rest.Route{"GET", "/events", HandleGetAllEvents},
		rest.Route{"GET", "/status", getSystemStatus},
		rest.Route{"GET", "/static/*", handleStatic},
		rest.Route{"GET", "/bower_components/*", handleVendor},
		rest.Route{"POST", "/arm", handleArmSystem},
		rest.Route{"POST", "/disarm", handleDisarmSystem},
	)
	http.ListenAndServe(":8081", &handler)
}
