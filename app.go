package main

import (
	"encoding/binary"
	"encoding/json"
	"github.com/ant0ine/go-json-rest"
	"github.com/steveyen/gkvlite"
	"log"
	"net/http"
	"os"
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

var f os.File
var s gkvlite.Store
var dbname = "/tmp/test.db"

func setObj(collection string, key []byte, object interface{}) error {
	f2, err := os.OpenFile(dbname, os.O_RDWR, 0666)
	if err != nil {
		log.Println("Error while opening db", err)
		return err
	}
	s2, err := gkvlite.NewStore(f2)
	if err != nil {
		log.Println("Error while opening store", err)
		return err
	}

	c := s2.SetCollection(collection, nil)

	objectBytes, err := json.Marshal(object)
	if err != nil {
		log.Println("Error while marshalling object in collection", collection, object, err)
		return err
	}

	err = c.Set(key, objectBytes)
	if err != nil {
		log.Println("Error while saving object", key, collection, object, err)
		return err
	}
	// log.Println("Set", collection, key, object)
	if err := s2.Flush(); err != nil {
		log.Println("Error flushing changes to disk")
	} else {
		// log.Println("Flushed")
	}
	f2.Sync()
	return nil
}

func AddEvent(e Event) error {
	if e.Time == 0 {
		e.Time = uint64(time.Now().UnixNano())
	}
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, e.Time)
	return setObj("events", b, e)
}

func GetAllEvents() (error, []Event) {
	log.Println("getting all events")
	f2, err := os.Open(dbname)
	if err != nil || f2 == nil {
		log.Println("Error while opening db", err)
		return err, nil
	}
	s2, err := gkvlite.NewStore(f2)
	if err != nil {
		log.Println("Error while opening store", err)
		return err, nil
	}

	c := s2.GetCollection("events")
	maxItem, err := c.MaxItem(true)
	if err != nil {
		return err, nil
	}
	events := []Event{}
	if maxItem == nil {
		return nil, events
	}
	j := Event{}
	err = json.Unmarshal(maxItem.Val, &j)
	events = append(events, j)
	log.Println("Here", j)
	err = c.VisitItemsDescend(maxItem.Key, true, func(i *gkvlite.Item) bool {
		j := Event{}
		err := json.Unmarshal(i.Val, &j)
		if err != nil {
			log.Println("Error unmarshalling value", err)
		} else {
			events = append(events, j)
		}
		log.Println("Here", j)
		return true
	})

	if err != nil {
		log.Println("Error scanning table 'events'", err)
	}

	return nil, events
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

func GetUser(w *rest.ResponseWriter, req *rest.Request) {
	f2, err := os.Open(dbname)
	s2, err := gkvlite.NewStore(f2)
	c := s2.SetCollection("users", nil)
	//time.Now().Unix()

	var user User

	userBytes, err := c.Get([]byte(req.PathParam("id")))
	if err != nil {
		log.Fatal(err)
		return
	}
	if userBytes == nil {
		rest.NotFound(w, req)
		return
	}

	err = json.Unmarshal(userBytes, &user)
	if err != nil {
		log.Fatal(err)
		return
	}

	w.WriteJson(&user)

}
func main() {
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
