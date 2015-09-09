package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net"
	"fmt"
	"strings"
	"html/template"
	"net/http"
	"time"
	"strconv"
	"bytes"

	"github.com/nlopes/slack"
	"github.com/sendgrid/sendgrid-go"
	"gopkg.in/redis.v3"
)

var (
	addr = flag.Bool("addr", false, "find open address and print to final-port.txt")
)

type Config struct {
	port int
	redis_address string
	redis_password string
	redis_db int64
	slack_key string
	slack_channel string
	sendgrid_username string
	sendgrid_password string

	domain string

	email_from string
	email_subject string
}

var config = &Config{
	port: 8080,
	redis_address: "localhost:6379",
	redis_password: "",
	redis_db: 0,

	slack_key: "",
	slack_channel: "",

	sendgrid_username: "",
	sendgrid_password: "",

	domain: "",

	email_from: "hello@domain.com",
	email_subject: "Status Update",
}

var port int
var redis_address string
var slack_key string

var api = slack.New(config.slack_key)

var redisClient = redis.NewClient(&redis.Options{
	Addr:     config.redis_address,
	Password: config.redis_password, // no password set
	DB:       config.redis_db,  // use default DB
})

type Controller struct {

}

var templates = template.Must(template.ParseFiles("index.html", "email-template.html"))

type Message struct {
  Text string
  Timestamp string
  Success bool
}

type Page struct {
	Title string
	Messages []Message
	LatestMessage Message
}

type Emails struct {

}

func (c *Emails) add(email string) {
	err := redisClient.SAdd("email-subscribers", email).Err()

	if err != nil {
		fmt.Printf("%s\n", err)
	}
}

func (c *Emails) remove(email string) {
	redisClient.SRem("email-subscribers", email).Err()
}

func (c *Emails) list() []string {
	return redisClient.SMembers("email-subscribers").Val()
}

var emails = &Emails{}

// Statuses
func (c *Controller) index(w http.ResponseWriter, r *http.Request) {
	historyParams := slack.HistoryParameters{Count: 10} //&HistoryParameters{Latest: "", Oldest: "", Count: 10, Inclusive: true}
	historyStruct, err := api.GetChannelHistory(config.slack_channel, historyParams)
	if err != nil {
		fmt.Printf("%s\n", err)
		return
	}

	// Grab latest message to put at the top
	convertedTimestamp, _ := strconv.ParseInt(	historyStruct.Messages[0].Timestamp[0:10], 10, 64)
	timestamp := time.Unix(convertedTimestamp, 0).Format("Jan 2, 2006 at 3:04pm (CST)")

	latestMessage := &Message {
		Text: strings.Replace(historyStruct.Messages[0].Text, "success: ", "", 1),
	 	Timestamp: timestamp,
  		Success: strings.Contains(historyStruct.Messages[0].Text, "success"),
	}

	// Build the message history to be displaded
	ChannelHistory := []Message{}

	for index, message := range historyStruct.Messages {
		success := strings.Contains(message.Text, "success")
		if index > 0 && !success {
				// Convert unix timestamp to readable timestamp
			convertedTimestamp, _ := strconv.ParseInt(message.Timestamp[0:10], 10, 64)
			timestamp := time.Unix(convertedTimestamp, 0).Format("Jan 2, 2006 at 3:04pm (CST)")

			ChannelHistory = append(ChannelHistory, *&Message {Text: strings.Replace(message.Text, "success: ", "", 1), Timestamp: timestamp, Success: success})
		}
	}

	p := &Page{Title: "Status Page", Messages: ChannelHistory, LatestMessage: *latestMessage}

	templates.ExecuteTemplate(w, "index.html", p)
}

// Add email to subscriptions
func (c *Controller) addEmail(w http.ResponseWriter, r *http.Request) {
	emailToAdd := r.URL.Query().Get("email")

	if emailToAdd != "" {
		emails.add(emailToAdd)
	}

	http.Redirect(w, r, "/", 307)
}

// Print emails in list
func (c *Controller) emailsInList(w http.ResponseWriter, r *http.Request) {
	emailsInList := emails.list()

	for _, email := range emailsInList {
		fmt.Printf("%s\n", email)
	}
}

type EmailData struct {
	Message string
	Email string
}

// Unsubscribe
func (c *Controller) unsubscribe(w http.ResponseWriter, r *http.Request) {
	emailToRemove := r.URL.Query().Get("email")
	emails.remove(emailToRemove)

	http.Redirect(w, r, "/", 301)
}

// Send email
func (c *Controller) sendEmail(w http.ResponseWriter, r *http.Request) {

	// Get latest message from slack
	historyParams := slack.HistoryParameters{Count: 1}
	historyStruct, err := api.GetChannelHistory("C095FQ6PM", historyParams)

	if err != nil {
		fmt.Printf("%s\n", err)
		return
	}

	latestMessage := strings.Replace(historyStruct.Messages[0].Text, "success: ", "", 1)

	fmt.Println(fmt.Sprint("Sending status: ", latestMessage))

	// Send an email to each subscriber with the latest message
	sg := sendgrid.NewSendGridClient(config.sendgrid_username, config.sendgrid_password);

	emailsInList := emails.list()

	for _, email := range emailsInList {
		go sendEmailToSubscriber(*sg, *&EmailData{Message: latestMessage, Email: email, Domain: config.domain})
	}
}


func sendEmailToSubscriber(sg sendgrid.SGClient, emailData EmailData) {
	message := sendgrid.NewMail()
	message.AddTo(emailData.Email)
	message.SetSubject(config.email_subject)
	message.SetFrom(config.email_from)

	var doc bytes.Buffer
	templates.ExecuteTemplate(&doc, "email-template.html", emailData)
	message.SetHTML(doc.String())

	if r := sg.Send(message); r == nil {
    	fmt.Println(fmt.Sprint("Email sent to: ", emailData.Email))
    } else {
        fmt.Println(r)
    }
}

func (c *Controller) updateStatus(w http.ResponseWriter, r *http.Request) {
	newStatus := r.URL.Query().Get("status")
	if newStatus != "" {
		redisClient.Set("status", newStatus, 0).Err()
	}
}

func main() {
	flag.Parse()

	var controllers = &Controller{}

	http.HandleFunc("/", controllers.index)
	http.HandleFunc("/update-status", controllers.updateStatus)
	http.HandleFunc("/add-email", controllers.addEmail)
	http.HandleFunc("/emails-in-list", controllers.emailsInList)
	http.HandleFunc("/send-email", controllers.sendEmail)
	http.HandleFunc("/unsubscribe", controllers.unsubscribe)

	if *addr {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		err = ioutil.WriteFile("final-port.txt", []byte(l.Addr().String()), 0644)
		if err != nil {
			log.Fatal(err)
		}
		s := &http.Server{}
		s.Serve(l)
		return
	}

	http.ListenAndServe(fmt.Sprint(":", config.port), nil)
}
