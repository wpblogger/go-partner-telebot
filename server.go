package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/buaazp/fasthttprouter"
	pgx "github.com/jackc/pgx/v4"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fastjson"
	"gopkg.in/tucnak/telebot.v2"
	tb "gopkg.in/tucnak/telebot.v2"
)

type loanType struct {
	Name     string `json:"name"`
	Codename string `json:"codename"`
}

type parentType struct {
	ID        int        `json:"id"`
	Name      string     `json:"name"`
	Codename  string     `json:"codename"`
	LoanTypes []loanType `json:"loan_types"`
}

type arrowStruct struct {
	Next string
	Prev string
}

type userStruct struct {
	OffersCategory []loanType
	Arrow          arrowStruct
	CurrentLink    string
}

var (
	branch       string
	db           *pgx.Conn
	ctxDb        context.Context
	b            *telebot.Bot
	userData     map[int]userStruct
	ramUser      map[int]int
	ramUserMutex = sync.RWMutex{}
	bonusURL     string
	djangoURL    string
)

func main() {
	var err error
	listenPort := "8012"
	if len(os.Getenv("GO_TELEGRAM_BOT_PORT")) > 0 {
		listenPort = os.Getenv("GO_TELEGRAM_BOT_PORT")
	}
	if len(os.Getenv("GO_TELEGRAM_BOT_BRANCH")) > 0 {
		branch = os.Getenv("GO_TELEGRAM_BOT_BRANCH")
	}
	var dbSource string
	if len(os.Getenv("POSTGRES_HOST")) > 0 && len(os.Getenv("POSTGRES_DATABASE")) > 0 && len(os.Getenv("POSTGRES_USER")) > 0 && len(os.Getenv("POSTGRES_PASSWORD")) > 0 && len(os.Getenv("POSTGRES_PORT")) > 0 {
		dbSource = "postgresql://" + os.Getenv("POSTGRES_USER") + ":" + os.Getenv("POSTGRES_PASSWORD") + "@" + os.Getenv("POSTGRES_HOST") + ":" + os.Getenv("POSTGRES_PORT") + "/" + os.Getenv("POSTGRES_DATABASE") + "?sslmode=disable"
	} else {
		log.Panic("ERROR: Env DB parameters are not set")
	}
	var telegramToken string
	if len(os.Getenv("TELEGRAM_TOKEN")) > 0 {
		telegramToken = os.Getenv("TELEGRAM_TOKEN")
	}
	bonusURL = ""
	if len(os.Getenv("WALLET_API_URL")) > 0 {
		bonusURL = os.Getenv("WALLET_API_URL")
	}
	djangoURL = ""
	if len(os.Getenv("DJANGO_API_URL")) > 0 {
		djangoURL = os.Getenv("DJANGO_API_URL")
	}
	ctxDb = context.Background()
	db, err = pgx.Connect(ctxDb, dbSource)
	if err != nil {
		log.Panic("Unable to connect to database: ", err)
	}
	defer db.Close(ctxDb)

	err = createTable()
	if err != nil {
		log.Panic("Users table is not available: ", err)
	}

	go func(listenPort string) {
		router := fasthttprouter.New()
		router.GET("/api/system/version", getVersion)
		server := &fasthttp.Server{
			Handler:            router.Handler,
			MaxRequestBodySize: 100 << 20,
			ReadBufferSize:     100 << 20,
		}
		log.Print("App start on port ", listenPort)
		log.Fatal(server.ListenAndServe(":" + listenPort))
	}(listenPort)

	userData = make(map[int]userStruct)
	ramUser = make(map[int]int)
	b, err = tb.NewBot(tb.Settings{
		URL:    "https://api.telegram.org",
		Token:  telegramToken,
		Poller: &tb.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Panic("Error, bot cannot start: ", err)
	}

	var gracefulStop = make(chan os.Signal)
	signal.Notify(gracefulStop, syscall.SIGTERM)
	signal.Notify(gracefulStop, syscall.SIGINT)
	go func() {
		sig := <-gracefulStop
		log.Printf("Received system call: %+v", sig)
		log.Print("Start shutdown App")
		db.Close(ctxDb)
		b.Stop()
		log.Print("App shutdown")
		os.Exit(0)
	}()

	var (
		selector       = &tb.ReplyMarkup{}
		btnChatURL     = selector.URL("Чат поддержки Юником24", "https://t.me/unicom24_support_bot")
		btnProfileURL  = selector.URL("Перейти в профиль", "https://unicom24.ru/partners/office/profile")
		btnExitConfirm = selector.Data("Да, я уверен", "exit", "")

		selectorOffers = &tb.ReplyMarkup{}
		btnPrev        = selectorOffers.Data("⬅", "prev", "")
		btnNext        = selectorOffers.Data("➡", "next", "")

		menuMain      = &tb.ReplyMarkup{ResizeReplyKeyboard: true}
		btnBalans     = menuMain.Text("Баланс")
		btnStatistic  = menuMain.Text("Статистика")
		btnOffersType = menuMain.Text("Офферы")
		btnChat       = menuMain.Text("Чат с менеджером")
		btnExit       = menuMain.Text("Выход")

		menuStatistic         = &tb.ReplyMarkup{ResizeReplyKeyboard: true}
		btnStatisticToday     = menuStatistic.Text("Сегодня")
		btnStatisticYesterday = menuStatistic.Text("Вчера")
		btnStatisticWeek      = menuStatistic.Text("Неделя")
		btnStatisticMonth     = menuStatistic.Text("Месяц")
		btnStatisticBack      = menuStatistic.Text("Назад")

		menuOffers = &tb.ReplyMarkup{ResizeReplyKeyboard: true}
		btnTest    = menuOffers.Text("Пагинация")

		menuExit = &tb.ReplyMarkup{ResizeReplyKeyboard: true}
		btnStart = menuExit.Text("/start")

		text string
	)
	menuMain.Reply(
		menuMain.Row(btnBalans, btnStatistic, btnOffersType),
		menuMain.Row(btnChat, btnExit),
	)

	menuStatistic.Reply(
		menuStatistic.Row(btnStatisticToday, btnStatisticYesterday),
		menuStatistic.Row(btnStatisticWeek, btnStatisticMonth, btnStatisticBack),
	)

	menuOffers.Reply(
		menuOffers.Row(btnTest),
	)

	menuExit.Reply(
		menuExit.Row(btnStart),
	)

	b.Handle("/start", func(m *tb.Message) {
		if !m.Private() {
			return
		}
		log.Print(m.Sender.ID, " -> Bot Request : ", m.Text)
		if checkUserRAM(m.Sender.ID) == 0 {
			selector.Inline(selector.Row(btnProfileURL))
			b.Send(m.Sender, "Чтобы начать, отправьте сообщение с командой для авторизации. Скопировать команду можно в личном кабинете: раздел «Профиль», вкладка «Основное».", selector)
		} else {
			b.Send(m.Sender, "Для получения нужной информации воспользуйтесь кнопками меню.", menuMain)
		}
	})

	b.Handle(tb.OnText, func(m *tb.Message) {
		log.Print(m.Sender.ID, " -> Request : ", m.Text)
		if checkUserRAM(m.Sender.ID) == 0 {
			if checkUserKey(m.Sender.ID, m.Text) {
				b.Send(m.Sender, "Вы успешно авторизованы. Для получения нужной информации воспользуйтесь кнопками меню.", menuMain)
			} else {
				selector.Inline(selector.Row(btnProfileURL))
				b.Send(m.Sender, "Неверная команда. Чтобы начать, отправьте сообщение с командой для авторизации. Скопировать команду можно в личном кабинете: раздел «Настройки».", selector)
			}
		} else {
			switch m.Text {
			case "Чат с менеджером":
				selector.Inline(
					selector.Row(btnChatURL),
				)
				b.Send(m.Sender, "Если у вас возникли трудности, напишите в чат поддержки Юником24.", selector)

			case "Выход":
				selector.Inline(
					selector.Row(btnExitConfirm),
				)
				b.Send(m.Sender, "Вы уверены, что хотите выйти ?", selector)

			case "Назад":
				b.Send(m.Sender, "Для получения нужной информации воспользуйтесь кнопками меню.", menuMain)

			case "Баланс":
				text, _ = getBalance(ramUser[m.Sender.ID], m.Sender.ID)
				b.Send(m.Sender, text, menuMain)

			case "Статистика":
				b.Send(m.Sender, "Выберите период", menuStatistic)

			case "Сегодня":
				text, _ = getStatistic(ramUser[m.Sender.ID], m.Sender.ID, "today")
				b.Send(m.Sender, text, menuStatistic)

			case "Вчера":
				text, _ = getStatistic(ramUser[m.Sender.ID], m.Sender.ID, "yesterday")
				b.Send(m.Sender, text, menuStatistic)

			case "Неделя":
				text, _ = getStatistic(ramUser[m.Sender.ID], m.Sender.ID, "week")
				b.Send(m.Sender, text, menuStatistic)

			case "Месяц":
				text, _ = getStatistic(ramUser[m.Sender.ID], m.Sender.ID, "month")
				b.Send(m.Sender, text, menuStatistic)

			case "Офферы":
				var buttons []tb.ReplyButton
				var rowButtons [][]tb.ReplyButton
				text, datas, _ := getOffersCategory(ramUser[m.Sender.ID], m.Sender.ID)
				for _, data := range datas {
					buttons = append(buttons, tb.ReplyButton{Text: data.Name})
				}
				buttons = append(buttons, tb.ReplyButton{Text: "Назад"})
				var i int
				var tmpButtons []tb.ReplyButton
				for _, tmpData := range buttons {
					tmpButtons = append(tmpButtons, tmpData)
					i++
					if i == 3 {
						rowButtons = append(rowButtons, tmpButtons)
						tmpButtons = nil
						i = 0
					}
				}
				rowButtons = append(rowButtons, tmpButtons)
				b.Send(m.Sender, text, &tb.ReplyMarkup{ReplyKeyboard: rowButtons, ResizeReplyKeyboard: true})

			default:
				for _, category := range userData[m.Sender.ID].OffersCategory {
					if category.Name == m.Text {
						url := djangoURL + "/v1/internal/offers/?partner_id=" + strconv.Itoa(ramUser[m.Sender.ID]) + "&page_size=5&loan_type=" + category.Codename
						thisData := userData[m.Sender.ID]
						thisData.Arrow.Prev = ""
						thisData.Arrow.Next = ""
						userData[m.Sender.ID] = thisData
						text, _ = getOfferPage(ramUser[m.Sender.ID], m.Sender.ID, url)
						nextLink := userData[m.Sender.ID].Arrow.Next
						prevLink := userData[m.Sender.ID].Arrow.Prev
						btnPrev = selectorOffers.Data("⬅", "prev", nextLink)
						btnNext = selectorOffers.Data("➡", "next", prevLink)
						if len(prevLink) > 0 && len(nextLink) > 0 {
							selectorOffers.Inline(selector.Row(btnPrev, btnNext))
						} else if len(prevLink) > 0 {
							selectorOffers.Inline(selector.Row(btnPrev))
						} else if len(nextLink) > 0 {
							selectorOffers.Inline(selector.Row(btnNext))
						} else {
							selectorOffers.Inline(selector.Row())
						}
						b.Send(m.Sender, text, selectorOffers, tb.ModeHTML, tb.NoPreview)
					}
				}
			}
		}
		log.Print(m.Sender.ID, " -> Bot Response : ", strings.ReplaceAll(text, "\n", " | "))
	})

	b.Handle(&btnPrev, func(c *tb.Callback) {
		b.Respond(c, &tb.CallbackResponse{
			ShowAlert: false,
		})
		var msgs tb.StoredMessage
		msgs.MessageID, msgs.ChatID = c.Message.MessageSig()
		url := djangoURL + "/offers/?" + userData[int(msgs.ChatID)].Arrow.Prev
		text, _ = getOfferPage(ramUser[int(msgs.ChatID)], int(msgs.ChatID), url)
		nextLink := userData[int(msgs.ChatID)].Arrow.Next
		prevLink := userData[int(msgs.ChatID)].Arrow.Prev
		btnPrev = selectorOffers.Data("⬅", "prev", prevLink)
		btnNext = selectorOffers.Data("➡", "next", nextLink)
		if len(prevLink) > 0 && len(nextLink) > 0 {
			selectorOffers.Inline(selector.Row(btnPrev, btnNext))
		} else if len(prevLink) > 0 {
			selectorOffers.Inline(selector.Row(btnPrev))
		} else if len(nextLink) > 0 {
			selectorOffers.Inline(selector.Row(btnNext))
		}
		b.Edit(msgs, text, selectorOffers, tb.ModeHTML)
	})

	b.Handle(&btnNext, func(c *tb.Callback) {
		b.Respond(c, &tb.CallbackResponse{
			ShowAlert: false,
		})
		var msgs tb.StoredMessage
		msgs.MessageID, msgs.ChatID = c.Message.MessageSig()
		url := djangoURL + "/offers/?" + userData[int(msgs.ChatID)].Arrow.Next
		text, _ = getOfferPage(ramUser[int(msgs.ChatID)], int(msgs.ChatID), url)
		nextLink := userData[int(msgs.ChatID)].Arrow.Next
		prevLink := userData[int(msgs.ChatID)].Arrow.Prev
		btnPrev = selectorOffers.Data("⬅", "prev", prevLink)
		btnNext = selectorOffers.Data("➡", "next", nextLink)
		if len(prevLink) > 0 && len(nextLink) > 0 {
			selectorOffers.Inline(selector.Row(btnPrev, btnNext))
		} else if len(prevLink) > 0 {
			selectorOffers.Inline(selector.Row(btnPrev))
		} else if len(nextLink) > 0 {
			selectorOffers.Inline(selector.Row(btnNext))
		}
		b.Edit(msgs, text, selectorOffers, tb.ModeHTML)
	})

	b.Handle(&btnExitConfirm, func(c *tb.Callback) {
		b.Respond(c, &tb.CallbackResponse{
			ShowAlert: false,
		})
		var msgs tb.StoredMessage
		msgs.MessageID, msgs.ChatID = c.Message.MessageSig()
		_ = delUserDB(c.Sender.ID)
		b.Send(c.Sender, "Вы успешно вышли", menuExit)
	})

	b.Start()
}

func getVersion(ctx *fasthttp.RequestCtx) {
	ctx.Response.Header.Set("Content-Type", "application/json")
	fmt.Fprint(ctx, `{"data": {"version": "`+branch+`"}, "error": {}}`)
}

func getURLParams(url string) string {
	r, _ := regexp.Compile(`offers\/\?(.*)`)
	result := r.FindStringSubmatch(url)
	if len(result) > 1 {
		return result[1]
	}
	return ""
}

func checkUserRAM(telegramID int) int {
	var partnerID int
	if ramUser[telegramID] == 0 {
		partnerID = checkUserDB(telegramID)
	} else {
		partnerID = ramUser[telegramID]
		log.Print(telegramID, " -> PartnerID = ", partnerID, " found in Cache")
	}
	return partnerID
}

func checkUserKey(telegramID int, key string) bool {
	match, err := regexp.MatchString("^[a-z0-9]{20}$", key)
	if err != nil {
		log.Print(err)
		return false
	}
	if match {
		partnerID := checkUserAPI(key, telegramID)
		if partnerID != 0 {
			ramUserMutex.Lock()
			ramUser[telegramID] = partnerID
			ramUserMutex.Unlock()
			if checkUserDB(telegramID) != partnerID {
				if !addUserDB(telegramID, partnerID) {
					ramUserMutex.Lock()
					delete(ramUser, telegramID)
					ramUserMutex.Unlock()
					return false
				}
			}
			return true
		}
	}
	return false
}

func checkUserDB(telegramID int) int {
	var partnerID int
	err := db.QueryRow(ctxDb, "select partner_id from users where telegram_id = $1", telegramID).Scan(&partnerID)
	if err != nil {
		if err == pgx.ErrNoRows {
			log.Print(telegramID, " -> Not found partnerID in the DB")
		} else {
			log.Print(telegramID, " -> DB search ERROR: ", err)
		}
	}
	ramUserMutex.Lock()
	ramUser[telegramID] = partnerID
	ramUserMutex.Unlock()
	log.Print(telegramID, " -> PartnerID = ", partnerID, " found in DB")
	return partnerID
}

func addUserDB(telegramID int, partnerID int) bool {
	var id int
	err := db.QueryRow(ctxDb, "insert into users (partner_id,telegram_id) values ($1, $2) returning id", partnerID, telegramID).Scan(&id)
	if err != nil {
		log.Print(telegramID, " -> PartnerID = ", partnerID, " add to DB, ERROR: ", err)
	}
	if id != 0 {
		log.Print(telegramID, " -> PartnerID = ", partnerID, " add to DB")
		return true
	}
	return false
}

func delUserDB(telegramID int) bool {
	var id int
	err := db.QueryRow(ctxDb, "delete from users where telegram_id = $1 returning id", telegramID).Scan(&id)
	if err != nil {
		log.Print(telegramID, " -> Delete from DB, ERROR: ", err)
	}
	if id != 0 {
		ramUserMutex.Lock()
		delete(ramUser, telegramID)
		ramUserMutex.Unlock()
		log.Print(telegramID, " Delete from DB")
		return true
	}
	return false
}

func checkUserAPI(key string, telegramID int) int {
	var partnerID int
	body, err := requestAPI(djangoURL+"/partner/"+key+"/", "GET", telegramID)
	if err == nil {
		if fastjson.GetBool(body, "is_active") {
			partnerID = fastjson.GetInt(body, "id")
		}
	}
	return partnerID
}

func requestAPI(url string, method string, telegramID int) ([]byte, error) {
	var body []byte
	var err error
	log.Print(telegramID, " -> Request : ", url)
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(url)
	req.Header.SetConnectionClose()
	req.Header.SetMethod(method)
	client := &fasthttp.Client{MaxIdleConnDuration: 10 * time.Second}
	client.Do(req, resp)
	if resp.StatusCode() != fasthttp.StatusOK || resp.Header.ContentLength() == 0 {
		err = errors.New("Error StatusCode = " + strconv.Itoa(resp.StatusCode()) + ", ContentLength = " + strconv.Itoa(resp.Header.ContentLength()))
		log.Print(telegramID, " -> Response ERROR : ", err)
	} else {
		body = resp.Body()
		log.Print(telegramID, " -> Response : ", string(body))
	}
	return body, err
}

func getRound(x float64) float64 {
	t := math.Trunc(x)
	if math.Abs(x-t) >= 0.5 {
		return t + math.Copysign(1, x)
	}
	return t
}

func getBalance(partnerID int, telegramID int) (string, error) {
	var err error
	var text string
	var coin int = -1
	coin = getUnicoin(partnerID, telegramID)
	body, err := requestAPI(djangoURL+"/partner/"+strconv.Itoa(partnerID)+"/balance/", "GET", telegramID)
	if err == nil {
		text = "Информация о балансе.\nБаланс: " + fmt.Sprintf("%.0f", getRound(fastjson.GetFloat64(body, "withdrawal_marked"))) + " ₽"
		text += "\nНа проверке: " + fmt.Sprintf("%.0f", getRound(fastjson.GetFloat64(body, "hold"))) + " ₽"
		if coin != -1 {
			text += "\nЮникоины: " + strconv.Itoa(coin)
		}
	}
	return text, err
}

func getUnicoin(partnerID int, telegramID int) int {
	var count int = -1
	var body []byte
	var err error
	body, err = requestAPI(bonusURL+"/wallet/"+strconv.Itoa(partnerID)+"?currency=unicoin", "GET", telegramID)
	if err != nil {
		return -1
	}
	count = int(fastjson.GetFloat64(body, "data", "results", "0", "balance"))
	return count
}

func getStatistic(partnerID int, telegramID int, period string) (string, error) {
	var param string
	var dates string
	var text string
	switch period {
	case "today":
		dates = time.Now().Format("02.01.2006")
		param = "date=" + time.Now().Format("2006-01-02")
	case "yesterday":
		dates = time.Now().Add(-24 * time.Hour).Format("02.01.2006")
		param = "date=" + time.Now().Add(-24*time.Hour).Format("2006-01-02")
	case "week":
		dates = time.Now().Add(-8*24*time.Hour).Format("02.01.2006") + " - " + time.Now().Add(-24*time.Hour).Format("02.01.2006")
		param = "date_from=" + time.Now().Add(-8*24*time.Hour).Format("2006-01-02") + "&date_to=" + time.Now().Add(-24*time.Hour).Format("2006-01-02")
	case "month":
		dates = time.Now().Add(-31*24*time.Hour).Format("02.01.2006") + " - " + time.Now().Add(-24*time.Hour).Format("02.01.2006")
		param = "date_from=" + time.Now().Add(-31*24*time.Hour).Format("2006-01-02") + "&date_to=" + time.Now().Add(-24*time.Hour).Format("2006-01-02")
	}
	body, err := requestAPI(djangoURL+"/v1/internal/partners/"+strconv.Itoa(partnerID)+"/stat/?"+param, "GET", telegramID)
	if err == nil {
		text = "Статистика за период " + dates
		text += "\nУникальные клики: " + strconv.Itoa(fastjson.GetInt(body, "total_count_unique"))
		text += "\nОдобренные: " + strconv.Itoa(fastjson.GetInt(body, "approvals_count"))
		text += "\nОтклоненные: " + strconv.Itoa(fastjson.GetInt(body, "refusals_count"))
		text += "\nВ работе: " + strconv.Itoa(fastjson.GetInt(body, "expected_count"))
		text += "\nEPL: " + fmt.Sprintf("%.1f", fastjson.GetFloat64(body, "EPL")) + " ₽"
		text += "\nEPC: " + fmt.Sprintf("%.1f", fastjson.GetFloat64(body, "EPC")) + " ₽"
		text += "\nCR: " + fmt.Sprintf("%.1f", fastjson.GetFloat64(body, "CR")) + " %"
		text += "\nAR: " + fmt.Sprintf("%.1f", fastjson.GetFloat64(body, "AR")) + " %"
		text += "\nНачислено: " + fmt.Sprintf("%.0f", getRound(fastjson.GetFloat64(body, "earnings"))) + " ₽"
	}
	return text, err
}

func getOffersCategory(partnerID int, telegramID int) (string, []loanType, error) {
	var text string
	var err error
	var datas []parentType
	var newDatas []loanType
	body, err := requestAPI(djangoURL+"/categories/?partner_id="+strconv.Itoa(partnerID), "GET", telegramID)
	if err == nil {
		if err = json.Unmarshal(body, &datas); err != nil {
			log.Print(err)
			return text, newDatas, err
		}
		for _, parent := range datas {
			for _, child := range parent.LoanTypes {
				newDatas = append(newDatas, child)
			}
		}
	}
	if len(newDatas) > 0 {
		thisData := userData[telegramID]
		thisData.OffersCategory = newDatas
		userData[telegramID] = thisData
		text = "Выберите тип продукта"
	} else {
		text = "Продуктов не найдено"
	}
	return text, newDatas, err
}

func getOfferPage(partnerID int, telegramID int, url string) (string, error) {
	var text string
	var err error
	var next string
	var prev string
	body, err := requestAPI(url, "GET", telegramID)
	if err == nil {
		if fastjson.GetInt(body, "count") > 0 {
			next = fastjson.GetString(body, "next")
			prev = fastjson.GetString(body, "previous")
			thisData := userData[telegramID]
			thisData.Arrow.Prev = getURLParams(prev)
			thisData.Arrow.Next = getURLParams(next)
			userData[telegramID] = thisData
		}
		text += "Найдено <b>" + strconv.Itoa(fastjson.GetInt(body, "count")) + "</b> офферов"
		var i int
		for {
			if fastjson.GetInt(body, "results", strconv.Itoa(i), "id") == 0 {
				break
			}
			text += "\n\n<b>" + fastjson.GetString(body, "results", strconv.Itoa(i), "name") + "</b>"
			text += "\n<i>Вознаграждение:</i>"
			var k int
			for {
				if len(fastjson.GetString(body, "results", strconv.Itoa(i), "revenues", strconv.Itoa(k), "type_name")) == 0 {
					break
				}
				text += "\n" + fmt.Sprintf("%.0f", getRound(fastjson.GetFloat64(body, "results", strconv.Itoa(i), "revenues", strconv.Itoa(k), "payment"))) + "₽ - "
				text += fastjson.GetString(body, "results", strconv.Itoa(i), "revenues", strconv.Itoa(k), "type_name") + "."
				k++
			}
			text += "\nСсылка на оффер: " + fastjson.GetString(body, "results", strconv.Itoa(i), "redirect_link", "short")
			i++
		}
	} else {
		text = "Предложений не найдено."
	}
	return text, err
}

func createTable() error {
	var count int
	err := db.QueryRow(ctxDb, "select count(*) from information_schema.tables where table_schema='public' and table_name='users'").Scan(&count)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = nil
			log.Print("Checking the table in the DB -> Not Found")
		} else {
			log.Print("Checking the table in the DB -> ERROR: ", err)
		}
	}
	if count == 0 {
		_, err = db.Exec(ctxDb, "CREATE TABLE IF NOT EXISTS users ( id BIGSERIAL PRIMARY key, partner_id Bigint NOT NULL, telegram_id Bigint NOT NULL, constraint pk_users_telegram_id unique (telegram_id))")
		if err != nil {
			log.Print("Creating a table in the DB -> ERROR: ", err)
		} else {
			log.Print("Creating a table in the DB -> OK")
		}
	} else {
		log.Print("Checking the table in the DB -> OK")
	}
	return err
}
