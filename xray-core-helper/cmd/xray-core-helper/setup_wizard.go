package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"home-router/xray-core-helper/internal/api"
	"home-router/xray-core-helper/internal/setup"
)

func choose(input *bufio.Scanner, out io.Writer, question string, options []string, defaultChoice int) (int, bool) {
	for {
		fmt.Fprintln(out, question)
		for i, option := range options {
			fmt.Fprintf(out, "  %d) %s\n", i+1, option)
		}
		fmt.Fprintf(out, "Введите номер (Enter — %d): ", defaultChoice)
		if !input.Scan() {
			fmt.Fprintln(out, "\nВвод завершён.")
			return 0, false
		}
		text := strings.TrimSpace(input.Text())
		if text == "" {
			return defaultChoice, true
		}
		value, err := strconv.Atoi(text)
		if err == nil && value >= 1 && value <= len(options) {
			return value, true
		}
		fmt.Fprintln(out, "Введите номер одного из вариантов.")
	}
}

func runSetupWizard(client *http.Client, stdin io.Reader, out, stderr io.Writer) int {
	input := bufio.NewScanner(stdin)
	for {
		var status api.SetupStatusResponse
		if err := requestJSON(client, http.MethodGet, "/v1/setup", nil, &status); err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		if status.RecoveryRequired || status.MutationsBlocked {
			fmt.Fprintln(stderr, "Требуется восстановление; проверьте setup --status и safe-apply status.")
			return 1
		}
		if !status.SetupRequired {
			fmt.Fprintln(out, "Helper уже настроен. Существующие настройки сохранены.")
			return 0
		}
		if status.ConfigState == "existing" {
			answer, ok := choose(input, out, "Настроить helper для установленного Xray?\nСуществующие настройки будут проверены автоматически.", []string{"Да, продолжить", "Настроить позже"}, 1)
			if !ok || answer == 2 {
				return 0
			}
		}
		answer, ok := choose(input, out, "Настроить перенаправление трафика через Xray (TProxy)?\nУдалённый прокси можно подключить позже. Включение сети потребует отдельного подтверждения.", []string{"Да, настроить сейчас", "Нет, настроить позже"}, 1)
		if !ok {
			return 0
		}
		if answer == 2 {
			if status.ConfigState != "existing" {
				fmt.Fprintln(out, "Установка компонентов завершена. Setup не завершён: конфиг ещё не создан. Продолжить: sudo xray-core-helper setup")
				return 0
			}
			confirm, ok := choose(input, out, "Подключить helper к существующему Xray?\nНастройки Xray и сети сохранятся, перезапуск не потребуется.", []string{"Подключить", "Изменить выбор", "Настроить позже"}, 3)
			if !ok || confirm == 3 {
				return 0
			}
			if confirm == 2 {
				continue
			}
			var result setup.Result
			if err := requestJSON(client, http.MethodPost, "/v1/setup/adopt", map[string]string{"configFingerprint": status.ConfigFingerprint}, &result); err != nil {
				writeRequestError(stderr, "setup", err)
				return 1
			}
			fmt.Fprintln(out, "Xray подключён. Существующий конфиг и сеть сохранены; TProxy не активировался.")
			return 0
		}
		var choices api.SetupInterfacesResponse
		if err := requestJSON(client, http.MethodGet, "/v1/setup/interfaces", nil, &choices); err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		var items []api.SetupInterface
		var options []string
		for _, item := range choices.Interfaces {
			if item.Excluded && item.Reason != "virtual" {
				continue
			}
			items = append(items, item)
			options = append(options, item.Name+" — "+formatSetupAddresses(item.Addresses))
		}
		options = append(options, "Вернуться")
		defaultInterface := len(options)
		if len(items) == 1 {
			defaultInterface = 1
		}
		selected, ok := choose(input, out, "Через какой интерфейс поступает трафик устройств вашей сети?", options, defaultInterface)
		if !ok {
			return 0
		}
		if selected == len(options) {
			continue
		}
		item := items[selected-1]
		var addresses []string
		for _, address := range item.Addresses {
			if !strings.Contains(address, ":") {
				addresses = append(addresses, address)
			}
		}
		if len(addresses) == 0 {
			fmt.Fprintln(out, "У интерфейса нет IPv4-адреса. Вернитесь после настройки адреса ОС.")
			continue
		}
		var addressChoices []string
		for _, address := range addresses {
			addressChoices = append(addressChoices, formatSetupAddresses([]string{address}))
		}
		addressChoices = append(addressChoices, "Вернуться")
		defaultAddress := len(addressChoices)
		if len(addresses) == 1 {
			defaultAddress = 1
		}
		selected, ok = choose(input, out, "Какой адрес сервера и подсеть использовать?", addressChoices, defaultAddress)
		if !ok {
			return 0
		}
		if selected == len(addressChoices) {
			continue
		}
		address := strings.SplitN(addresses[selected-1], "/", 2)[0]
		plan, err := requestSetupPlan(client, item.Name, address)
		if err != nil && strings.Contains(err.Error(), "preflight_xray_gateway_review_required") {
			answer, ok := choose(input, out, "Существующий Xray нельзя безопасно дополнить автоматически. Заменить конфиг установочным шаблоном? Текущие подключения и правила будут заменены; резервная копия сохранится.", []string{"Заменить шаблоном", "Отложить и разобрать вручную"}, 2)
			if !ok || answer == 2 {
				return 0
			}
			var response struct {
				Plan setup.Plan `json:"plan"`
			}
			err = requestJSON(client, http.MethodPost, "/v1/setup/plan", api.SetupPlanRequest{Interface: item.Name, Address: address, ReplaceXray: true}, &response)
			plan = response.Plan
		}
		if err != nil {
			writeRequestError(stderr, "setup", err)
			return 1
		}
		printSetupPlan(out, plan, false)
		confirm, ok := chooseSetupPlan(input, out, plan)
		if !ok || confirm == 3 {
			return 0
		}
		if confirm == 2 {
			continue
		}
		var result setup.Result
		fmt.Fprintln(out, "Подготавливаем настройки Xray и сети…")
		if err := requestJSON(client, http.MethodPost, "/v1/setup/apply", plan, &result); err != nil {
			writeRequestError(stderr, "setup; проверьте общий статус перед повтором", err)
			return 1
		}
		return offerNetworkActivation(client, input, out, stderr)
	}
}

// Activation uses the existing durable safe-apply journal and grouped recovery.
func offerNetworkActivation(client *http.Client, input *bufio.Scanner, out, stderr io.Writer) int {
	fmt.Fprintln(out, "\nНастройки подготовлены, Xray проверен. Перенаправление трафика пока выключено.\nПри включении проверим доступ в интернет до и после изменения сети.\nДержите открытой независимую консоль. Для завершения отката потребуется ручная перезагрузка.")
	answer, ok := choose(input, out, "Включить перенаправление трафика через Xray?", []string{"Да, включить и проверить", "Нет, оставить выключенным"}, 2)
	if !ok || answer == 2 {
		fmt.Fprintln(out, "Настройки подготовлены. Перенаправление трафика оставлено выключенным.")
		return 0
	}
	var backups api.TemplateBackupsResponse
	var err error
	// Setup hands off to the normal helper asynchronously. Only reads are retried.
	for attempt := 0; attempt < 20; attempt++ {
		err = requestJSON(client, http.MethodGet, "/v1/templates/tproxy-unit/backups", nil, &backups)
		if err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		writeRequestError(stderr, "activation unavailable; setup is complete", err)
		return 1
	}
	var applied api.ApplyTemplateResponse
	printNetworkProbe(client, out, api.NetworkProbeRequest{Stage: "before"})
	if err = requestJSON(client, http.MethodPost, "/v1/templates/tproxy-unit/apply", api.ApplyTemplateRequest{ExpectedGeneration: &backups.Generation}, &applied); err != nil {
		writeRequestError(stderr, "activation; inspect safe-apply status before retry", err)
		return 1
	}
	fmt.Fprintf(out, "Перенаправление включено временно. Подтвердите сохранение до %s.\nБез подтверждения начнётся откат; для его завершения потребуется ручная перезагрузка.\nПроверка интернета выполняется с сервера. Соединения других устройств проверьте отдельно.\n", applied.Deadline.Local().Format("02.01.2006 15:04:05 MST"))
	printNetworkProbe(client, out, api.NetworkProbeRequest{Stage: "after", TransactionID: applied.TransactionID})
	var choice int
	for {
		choice, ok = choose(input, out, "Сохранить настройки и включать TProxy при загрузке?\nВы можете сохранить их и при неуспешной проверке интернета.", []string{"Да, сохранить", "Откатить изменения", "Проверить интернет ещё раз", "Не подтверждать — дождаться отката"}, 4)
		if !ok || choice != 3 {
			break
		}
		printNetworkProbe(client, out, api.NetworkProbeRequest{Stage: "after", TransactionID: applied.TransactionID})
	}
	if !ok || choice == 4 {
		fmt.Fprintln(out, "Настройки не подтверждены. После истечения срока начнётся откат.\nДля проверки состояния: sudo xray-core-helper safe-apply status\nДля завершения отката потребуется ручная перезагрузка.")
		return 0
	}
	action := "confirm"
	if choice == 2 {
		action = "rollback"
	}
	var result api.SafeApplyResolutionResponse
	if err = requestJSON(client, http.MethodPost, "/v1/safe-apply/"+applied.TransactionID+"/"+action, struct{}{}, &result); err != nil {
		writeRequestError(stderr, "safe-apply", err)
		return 1
	}
	if action == "confirm" {
		var enabled api.ServiceActionResponse
		if err = requestJSON(client, http.MethodPost, "/v1/xray/services/tproxy/enable", struct{}{}, &enabled); err != nil {
			writeRequestError(stderr, "TProxy confirmed; boot enable failed", err)
			return 1
		}
		fmt.Fprintln(out, "Настройки сохранены. Перенаправление трафика через Xray включено, автозапуск включён.")
	}
	if result.Transaction.RebootRequired {
		fmt.Fprintln(out, "Файлы восстановлены. Требуется ручная перезагрузка; эффекты работающего ядра не восстановлены.")
	}
	return 0
}

func printNetworkProbe(client *http.Client, out io.Writer, request api.NetworkProbeRequest) {
	stage := "до включения"
	if request.Stage == "after" {
		stage = "после включения"
	}
	fmt.Fprintf(out, "\nПроверяем доступ в интернет %s…\n", stage)
	var result api.NetworkProbeResult
	if err := requestJSON(client, http.MethodPost, "/v1/network/probe", request, &result); err != nil {
		fmt.Fprintln(out, "Не удалось получить результат проверки. Вы можете повторить проверку или принять решение вручную.")
		return
	}
	printNetworkProbeResult(out, result)
}

func formatSetupAddresses(addresses []string) string {
	var labels []string
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address)
		if err != nil {
			labels = append(labels, address)
			continue
		}
		labels = append(labels, fmt.Sprintf("адрес %s, подсеть %s", prefix.Addr(), prefix.Masked()))
	}
	return strings.Join(labels, "; ")
}

func printSetupPlan(out io.Writer, plan setup.Plan, details bool) {
	if details {
		fmt.Fprintln(out, "\nТехнические подробности")
		if plan.Gateway != nil {
			fmt.Fprintf(out, "TProxy TCP/UDP: порт %d; исходящая марка %d.\nИзменения Xray: %s.\n", plan.Gateway.Port, plan.Gateway.EgressMark, strings.Join(plan.Gateway.Changes, ", "))
		}
		fmt.Fprintln(out, "Сетевые файлы: nftables, sysctl, systemd. Автозапуск включается после сохранения работающей сети.")
		return
	}
	fmt.Fprintf(out, "\nПроверьте выбранные настройки\nИнтерфейс: %s\nАдрес сервера: %s\nПодсеть: %s\n", plan.Interface, plan.ListenAddress, plan.LANNetwork)
	if plan.ReplaceXray {
		fmt.Fprintln(out, "Конфигурация Xray будет заменена установочным шаблоном; резервная копия сохранится.")
	} else if plan.PreserveXray {
		fmt.Fprintln(out, "Настройки Xray сохранятся. Сервис будет проверен и при необходимости запущен или перезапущен.")
	} else {
		fmt.Fprintln(out, "Будут подготовлены настройки Xray и сетевые правила. Xray будет перезапущен; существующая конфигурация сохранится в резервной копии.")
	}
	if plan.Gateway != nil && plan.Gateway.ProxyState == "direct-placeholder" {
		fmt.Fprintln(out, "Удалённый прокси не настроен: пока трафик выходит в интернет напрямую через Xray.")
	}
	fmt.Fprintln(out, "Перенаправление трафика останется выключенным до следующего подтверждения.\nЕсли на сервере уже есть правила межсетевого экрана, проверьте их совместимость: автоматически она не проверяется.")
}

func chooseSetupPlan(input *bufio.Scanner, out io.Writer, plan setup.Plan) (int, bool) {
	for {
		answer, ok := choose(input, out, "Подготовить эти настройки?", []string{"Да, подготовить", "Изменить выбор", "Настроить позже", "Подробнее об изменениях"}, 3)
		if !ok || answer != 4 {
			return answer, ok
		}
		printSetupPlan(out, plan, true)
	}
}

func printNetworkProbeResult(out io.Writer, result api.NetworkProbeResult) {
	if result.Reachable {
		fmt.Fprintln(out, "Доступ в интернет работает.")
	} else {
		fmt.Fprintln(out, "Проверка не подтвердила доступ в интернет.")
	}
	switch result.DNS.Status {
	case "ok":
		fmt.Fprintln(out, "DNS: имя проверочного сайта определено.")
	case "failed":
		fmt.Fprintln(out, "DNS: не удалось определить адрес проверочного сайта.")
	default:
		fmt.Fprintln(out, "DNS: результат недоступен.")
	}
	switch result.GatewayPing.Status {
	case "ok":
		fmt.Fprintf(out, "Шлюз %s отвечает на ping.\n", result.Gateway)
	case "failed":
		fmt.Fprintf(out, "Шлюз %s не ответил на ping. Это само по себе не означает отсутствие интернета.\n", result.Gateway)
	default:
		fmt.Fprintln(out, "Ответ шлюза проверить не удалось.")
	}
	if result.HTTPStatus != 0 {
		fmt.Fprintf(out, "Проверочный сайт: %s — HTTP %d (ожидается %d).\n", result.URL, result.HTTPStatus, result.ExpectedStatus)
	} else {
		fmt.Fprintf(out, "Проверочный сайт: %s — ответ не получен.\n", result.URL)
	}
	fmt.Fprintf(out, "Время проверки: %s; длительность: %.1f с.\n", result.CheckedAt.Local().Format("02.01.2006 15:04:05 MST"), float64(result.DurationMS)/1000)
}
