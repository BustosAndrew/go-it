package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"

	"github.com/sashabaranov/go-openai"
	"github.com/twilio/twilio-go/twiml"
)

func main() {
    port := os.Getenv("PORT")
	if port == "" {
		port = "8081" // Default port if not set
	}
   err := godotenv.Load()
   if err != nil {
       log.Fatal("cannot retrieve env file")
   }
   gin.SetMode(gin.ReleaseMode)
   app := gin.Default()
   app.Any("/llm-websocket/:call_id", Retellwshandler)
   app.POST("/twilio-webhook/:agent_id", Twiliowebhookhandler) 
   app.Run(":" + port)
}

func GetRetellAISecretKey() string {
	return os.Getenv("RETELL_API_KEY")
}

func GetOpenAISecretKey() string {
	return os.Getenv("OPENAI_API_KEY")
}

type RegisterCallRequest struct {
   AgentID                string `json:"agent_id"`
   AudioEncoding          string `json:"audio_encoding"`
   AudioWebsocketProtocol string `json:"audio_websocket_protocol"`
   SampleRate             int    `json:"sample_rate"`
}

type RegisterCallResponse struct {
   AgentID                string `json:"agent_id"`
   AudioEncoding          string `json:"audio_encoding"`
   AudioWebsocketProtocol string `json:"audio_websocket_protocol"`
   CallID                 string `json:"call_id"`
   CallStatus             string `json:"call_status"`
   SampleRate             int    `json:"sample_rate"`
   StartTimestamp         int    `json:"start_timestamp"`
}

func Twiliowebhookhandler(c *gin.Context) {
   agent_id := c.Param("agent_id")

   callinfo, err := RegisterRetellCall(agent_id)
   if err != nil {
       c.JSON(http.StatusInternalServerError, "cannot handle call atm")
       return
   }

   twilloresponse := &twiml.VoiceStream{
       Url: "wss://api.retellai.com/audio-websocket/" + callinfo.CallID,
   }

   twiliostart := &twiml.VoiceConnect{
       InnerElements: []twiml.Element{twilloresponse},
   }

   twimlResult, err := twiml.Voice([]twiml.Element{twiliostart})
   if err != nil {
       c.JSON(http.StatusInternalServerError, "cannot handle call atm")
       return
   }

   c.Header("Content-Type", "text/xml")
   c.String(http.StatusOK, twimlResult)
}

func RegisterRetellCall(agent_id string) (RegisterCallResponse, error) {
   request := RegisterCallRequest{
       AgentID:                agent_id,
       AudioEncoding:          "mulaw",
       SampleRate:             8000,
       AudioWebsocketProtocol: "twilio",
   }

   request_bytes, err := json.Marshal(request)
   if err != nil {
       return RegisterCallResponse{}, err
   }

   payload := bytes.NewBuffer(request_bytes)

   request_url := "https://api.retellai.com/register-call"
   method := "POST"

   var bearer = "Bearer " + GetRetellAISecretKey()

   client := &http.Client{}
   req, err := http.NewRequest(method, request_url, payload)
   if err != nil {
       return RegisterCallResponse{}, err
   }

   req.Header.Add("Authorization", bearer)
   req.Header.Add("Content-Type", "application/json")
   res, err := client.Do(req)
   if err != nil {
       return RegisterCallResponse{}, err
   }
   defer res.Body.Close()

   body, err := io.ReadAll(res.Body)
   if err != nil {
       return RegisterCallResponse{}, err
   }

   var response RegisterCallResponse

   json.Unmarshal(body, &response)

   return response, nil
}

type Transcripts struct {
   Role    string `json:"role"`
   Content string `json:"content"`
}

type Request struct {
   ResponseID      int           `json:"response_id"`
   Transcript      []Transcripts `json:"transcript"`
   InteractionType string        `json:"interaction_type"`
}
type Response struct {
   ResponseID      int    `json:"response_id"`
   Content         string `json:"content"`
   ContentComplete bool   `json:"content_complete"`
   EndCall         bool   `json:"end_call"`
}

func Retellwshandler(c *gin.Context) {
   upgrader := websocket.Upgrader{}

   upgrader.CheckOrigin = func(r *http.Request) bool {
       return true
   }

   conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
   if err != nil {
       log.Fatal(err)
   }

   response := Response{
       ResponseID:      0,
       Content:         "Hello, I am Susie, Andrew Tate's most trusted mistress. How can I help you get out of the matrix?",
       ContentComplete: true,
       EndCall:         false,
   }

   err = conn.WriteJSON(response)
   if err != nil {
       log.Fatal(err)
   }

   for {
       messageType, ms, err := conn.ReadMessage()
       if err != nil {
           conn.Close()

           break
       }

       if messageType == websocket.TextMessage {
           var msg Request
           json.Unmarshal(ms, &msg)
          
           HandleWebsocketMessages(msg, conn)
       }
   }
}

func HandleWebsocketMessages(msg Request, conn *websocket.Conn) {
   client := openai.NewClient(GetOpenAISecretKey())

   if msg.InteractionType == "update_only" {
       log.Println("update interaction, do nothing.")
       return
   }

   prompt := GenerateAIRequest(msg)

   req := openai.ChatCompletionRequest{
       Model:       openai.GPT3Dot5Turbo,
       Messages:    prompt,
       Stream:      true,
       MaxTokens:   200,
       Temperature: 1.0,
   }
   stream, err := client.CreateChatCompletionStream(context.Background(), req)
   if err != nil {
       log.Println(err)
       conn.Close()
   }

   defer stream.Close()
   var i int
   for {
       response, err := stream.Recv()
       if err != nil {
           var s string
           if (errors.Is(err, io.EOF) && i == 0) || (!errors.Is(err, io.EOF)) {
               s = "[ERROR] NO RESPONSE, PLEASE RETRY"
           }

           if errors.Is(err, io.EOF) && i != 0 {
               s = "\n\n###### [END] ######"
           }
           airesponse := Response{
               ResponseID:      msg.ResponseID,
               Content:         s,
               ContentComplete: false,
               EndCall:         false,
           }
         

           out, err := json.Marshal(airesponse)
           if err != nil {
               log.Println(err)
               conn.Close()
           }

           err = conn.WriteMessage(websocket.TextMessage, out)
           if err != nil {
               log.Println(err)
               conn.Close()
           }

           break
       }
       if len(response.Choices) > 0 {
           s := response.Choices[0].Delta.Content

           airesponse := Response{
               ResponseID:      msg.ResponseID,
               Content:         s,
               ContentComplete: false,
               EndCall:         false,
           }
           log.Println(airesponse)

           out, _ := json.Marshal(airesponse)

           err = conn.WriteMessage(websocket.TextMessage, out)
           if err != nil {
               log.Println(err)
               conn.Close()
           }
       }
       i = i + 1
   }
}

func GenerateAIRequest(msg Request) []openai.ChatCompletionMessage {
   var airequest []openai.ChatCompletionMessage

   systemprompt := openai.ChatCompletionMessage{
       Role:    "system",
       Content: "You are an AI phone assistant designed to embody the persona of an Andrew Tate-style motivator, delivering brutally honest, no-nonsense advice and roasts during conversations. Your tone is unapologetically confident, direct, and peppered with occasional cussing to emphasize points. You should aim to inspire action by challenging the user’s excuses and behaviors, often referencing concepts like ‘the Matrix,’ discipline, and personal accountability. If interrupted, seamlessly adjust your response to acknowledge the interruption while maintaining the same tone and persona. Use concise, impactful language, and adapt your replies to keep the energy of the conversation intense and engaging. Stay aligned with the meme-like nature of Andrew Tate-style conversations, keeping interactions entertaining, provocative, and sometimes borderline absurd—but never crossing the line into harmful or discriminatory behavior.",
   }

   airequest = append(airequest, systemprompt)

   for _, response := range msg.Transcript {
       var p_response openai.ChatCompletionMessage

       if response.Role == "agent" {
           p_response.Role = "assistant"
       } else {
           p_response.Role = "user"
       }

       p_response.Content = response.Content

       airequest = append(airequest, p_response)
   }

   return airequest
}