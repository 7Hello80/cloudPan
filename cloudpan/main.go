package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yuanshen-go/api"
	"yuanshen-go/config"
	"yuanshen-go/license"
	"yuanshen-go/storage"
	"yuanshen-go/update"

	"github.com/xuri/excelize/v2"
)

//go:embed templates/*
//go:embed static/*
var embeddedFS embed.FS

// 全局变量
var (
	store       *storage.Storage
	taskMgr     *api.TaskManager
	avatarCache *AvatarCache
	gachaAPI    *api.GachaAPI
)

// AvatarCache 头像缓存
type AvatarCache struct {
	cache  map[string]string
	mu     sync.RWMutex
	loaded bool
}

// NewAvatarCache 创建头像缓存
func NewAvatarCache() *AvatarCache {
	return &AvatarCache{
		cache: make(map[string]string),
	}
}

// Get 获取头像 URL
func (ac *AvatarCache) Get(name, itemType string) string {
	key := itemType + "_" + name

	ac.mu.RLock()
	if url, ok := ac.cache[key]; ok {
		ac.mu.RUnlock()
		return url
	}
	ac.mu.RUnlock()

	// 未找到时返回占位图
	return "https://ui-avatars.com/api/?name=" + name + "&background=random&size=128"
}

// Set 设置头像 URL
func (ac *AvatarCache) Set(name, itemType, url string) {
	key := itemType + "_" + name
	ac.mu.Lock()
	ac.cache[key] = url
	ac.mu.Unlock()
}

// LoadAsync 异步加载头像数据
func (ac *AvatarCache) LoadAsync() {
	if ac.loaded {
		return
	}
	go ac.loadFromAPI()
}

// loadFromAPI 从 API 加载头像数据
func (ac *AvatarCache) loadFromAPI() {
	// 创建自定义 DNS 解析器
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}
	
	// 尝试使用 Google DNS
	customResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "udp", "8.8.8.8:53")
		},
	}
	
	// 创建自定义 HTTP 客户端，使用自定义 DNS
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			
			// 使用自定义 DNS 解析
			ips, err := customResolver.LookupIPAddr(ctx, host)
			if err != nil {
				// 如果自定义 DNS 失败，尝试系统默认
				return dialer.DialContext(ctx, network, addr)
			}
			
			if len(ips) == 0 {
				return dialer.DialContext(ctx, network, addr)
			}
			
			// 使用第一个解析到的 IP，优先使用 IPv4
			var resolvedAddr string
			for _, ip := range ips {
				if ip.IP.To4() != nil {
					// IPv4 地址
					resolvedAddr = fmt.Sprintf("%s:%s", ip.IP.String(), port)
					break
				}
			}
			// 如果没有 IPv4，使用 IPv6（需要加方括号）
			if resolvedAddr == "" {
				resolvedAddr = fmt.Sprintf("[%s]:%s", ips[0].IP.String(), port)
			}
			
			return dialer.DialContext(ctx, network, resolvedAddr)
		},
		// 跳过 TLS 证书验证（解决 Termux 缺少 CA 证书的问题）
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	// 角色 API
	ac.loadAvatarFromAPI(client, "https://act-api-takumi-static.mihoyo.com/common/blackboard/ys_obc/v1/home/content/list?app_sn=ys_obc&channel_id=25", "角色")

	// 武器 API
	ac.loadAvatarFromAPI(client, "https://act-api-takumi-static.mihoyo.com/common/blackboard/ys_obc/v1/home/content/list?app_sn=ys_obc&channel_id=5", "武器")

	ac.loaded = true
}

// loadAvatarFromAPI 从指定 API 加载头像
func (ac *AvatarCache) loadAvatarFromAPI(client *http.Client, apiURL, itemType string) {
	resp, err := client.Get(apiURL)
	if err != nil {
		log.Printf("\033[1;31m加载%s头像失败: %v\033[0m", itemType, err)
		return
	}
	defer resp.Body.Close()

	var data struct {
		Retcode int `json:"retcode"`
		Data    struct {
			List []struct {
				List []struct {
					Title string `json:"title"`
					Icon  string `json:"icon"`
				} `json:"list"`
			} `json:"list"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("\033[1;31m解析%s头像数据失败: %v\033[0m", itemType, err)
		return
	}

	if data.Retcode != 0 || len(data.Data.List) == 0 {
		return
	}

	count := 0
	for _, item := range data.Data.List[0].List {
		if item.Title != "" && item.Icon != "" {
			ac.Set(item.Title, itemType, item.Icon)
			count++
		}
	}
	log.Printf("\033[1;32m已加载 %d 个%s头像\033[0m", count, itemType)
}

// 版本检查响应
type ReleaseInfo struct {
	TagName string `json:"tag_name"`
	HtmlURL string `json:"html_url"`
}

// compareVersions 比较版本号，返回 1 表示 v1 > v2，-1 表示 v1 < v2，0 表示相等
func compareVersions(v1, v2 string) int {
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i])
		}

		if n1 > n2 {
			return 1
		} else if n1 < n2 {
			return -1
		}
	}

	return 0
}

// PullRecord 抽卡记录
type PullRecord struct {
	Name          string `json:"name"`
	RankType      string `json:"rank_type"`
	ItemType      string `json:"item_type"`
	Time          string `json:"time"`
	Pulls         int    `json:"pulls"`
	PullsBefore   int    `json:"pulls_before"`
	PrimogemsCost int    `json:"primogems_cost"`
	AvatarURL     string `json:"avatar_url"`
	IsWai         bool   `json:"is_wai"`
}

// Stats 统计数据
type Stats struct {
	TotalPulls     int `json:"total_pulls"`
	TotalPrimogems int `json:"total_primogems"`
	FiveStarCount  int `json:"five_star_count"`
	FourStarCount  int `json:"four_star_count"`
	ThreeStarCount int `json:"three_star_count"`
	MatCount       int `json:"mat_count"`
}

// PoolData 卡池数据
type PoolData struct {
	Name          string             `json:"name"`
	Pulls         []PullRecord       `json:"pulls"`
	FourStarPulls []PullRecord       `json:"four_star_pulls"`
	AllRecords    []PullRecord       `json:"all_records"`
	Stats         Stats              `json:"stats"`
	RawData       []storage.GachaItem `json:"raw_data"`
}

// CalculatePityCounter 计算当前垫抽数
func CalculatePityCounter(gachaList []storage.GachaItem) int {
	if len(gachaList) == 0 {
		return 0
	}

	counter := 0
	for _, item := range gachaList {
		counter++
		if item.RankType == "5" {
			break
		}
	}
	if counter > 0 {
		return counter - 1
	}
	return 0
}

// CalculatePullRecords 计算五星抽卡记录
func CalculatePullRecords(gachaList []storage.GachaItem, gachaType string) []PullRecord {
	if len(gachaList) == 0 {
		return nil
	}

	var records []PullRecord
	currentPity := 0

	// 从最早到最晚遍历（需要反转）
	for i := len(gachaList) - 1; i >= 0; i-- {
		item := gachaList[i]
		currentPity++

		if item.RankType == "5" {
			itemName := item.GetName()
			records = append(records, PullRecord{
				Name:          itemName,
				RankType:      item.RankType,
				ItemType:      item.ItemType,
				Time:          item.Time,
				Pulls:         currentPity,
				PullsBefore:   currentPity,
				PrimogemsCost: currentPity * 160,
				AvatarURL:     avatarCache.Get(itemName, item.ItemType),
				IsWai:         config.IsStandardItem(itemName, item.ItemType, gachaType),
			})
			currentPity = 0
		}
	}

	return records
}

// CalculateFourStarRecords 计算四星抽卡记录
func CalculateFourStarRecords(gachaList []storage.GachaItem) []PullRecord {
	if len(gachaList) == 0 {
		return nil
	}

	var records []PullRecord
	currentPity := 0

	for i := len(gachaList) - 1; i >= 0; i-- {
		item := gachaList[i]
		currentPity++

		if item.RankType == "4" {
			itemName := item.GetName()
			records = append(records, PullRecord{
				Name:          itemName,
				RankType:      item.RankType,
				ItemType:      item.ItemType,
				Time:          item.Time,
				Pulls:         currentPity,
				PullsBefore:   currentPity,
				PrimogemsCost: currentPity * 160,
				AvatarURL:     avatarCache.Get(itemName, item.ItemType),
			})
			currentPity = 0
		}
	}

	return records
}

// CalculateStats 计算统计数据
func CalculateStats(gachaList []storage.GachaItem) Stats {
	if len(gachaList) == 0 {
		return Stats{}
	}

	stats := Stats{
		TotalPulls: len(gachaList),
	}

	for _, item := range gachaList {
		stats.TotalPrimogems += 160
		switch item.RankType {
		case "5":
			stats.FiveStarCount++
		case "4":
			stats.FourStarCount++
		case "3":
			stats.ThreeStarCount++
		}
	}

	stats.MatCount = CalculatePityCounter(gachaList)

	return stats
}

// ProcessBeyondGachaData 处理千星奇域数据
func ProcessBeyondGachaData(gachaList []storage.GachaItem) ([]PullRecord, Stats) {
	if len(gachaList) == 0 {
		return nil, Stats{}
	}

	// 按星级从高到低排序
	sortedList := make([]storage.GachaItem, len(gachaList))
	copy(sortedList, gachaList)

	sort.Slice(sortedList, func(i, j int) bool {
		rankI, _ := strconv.Atoi(sortedList[i].RankType)
		rankJ, _ := strconv.Atoi(sortedList[j].RankType)
		if rankI != rankJ {
			return rankI > rankJ
		}
		return sortedList[i].Time > sortedList[j].Time
	})

	// 计算各星级数量
	rankCounts := make(map[string]int)
	for _, item := range gachaList {
		rankCounts[item.RankType]++
	}

	// 构建显示记录
	var records []PullRecord
	for _, item := range sortedList {
		itemName := item.GetName()
		records = append(records, PullRecord{
			Name:      itemName,
			RankType:  item.RankType,
			ItemType:  item.ItemType,
			Time:      item.Time,
			AvatarURL: avatarCache.Get(itemName, item.ItemType),
		})
	}

	stats := Stats{
		TotalPulls:     len(gachaList),
		TotalPrimogems: len(gachaList) * 160,
	}

	return records, stats
}

// ProcessGachaData 处理单个卡池的数据
func ProcessGachaData(gachaList []storage.GachaItem, gachaType string) PoolData {
	poolData := PoolData{
		Name: config.GetGachaTypeName(gachaType),
	}

	// 千星奇域使用特殊处理
	if gachaType == "1000" || gachaType == "2000" {
		allRecords, stats := ProcessBeyondGachaData(gachaList)
		poolData.AllRecords = allRecords
		poolData.Stats = stats
		poolData.RawData = gachaList
		return poolData
	}

	poolData.Pulls = CalculatePullRecords(gachaList, gachaType)
	poolData.FourStarPulls = CalculateFourStarRecords(gachaList)
	poolData.Stats = CalculateStats(gachaList)
	poolData.RawData = gachaList

	return poolData
}

// Response 响应结构
type Response struct {
	Success   bool                   `json:"success"`
	Data      map[string]PoolData    `json:"data,omitempty"`
	UID       string                 `json:"uid,omitempty"`
	NewCounts map[string]int         `json:"new_counts,omitempty"`
	Users     []storage.UserInfo     `json:"users,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Message   string                 `json:"message,omitempty"`
}

// ProgressResponse 进度响应
type ProgressResponse struct {
	Name string `json:"name"`
	Page string `json:"page"`
}

// corsMiddleware CORS 中间件
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// logMiddleware 日志中间件
func logMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("[REQUEST] %s %s", r.Method, r.URL.Path)
		next(w, r)
		log.Printf("[RESPONSE] %s %s - %v", r.Method, r.URL.Path, time.Since(start))
	}
}

// handleIndex 首页
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// 尝试静态文件
		staticPath := strings.TrimPrefix(r.URL.Path, "/")
		data, err := embeddedFS.ReadFile(staticPath)
		if err == nil {
			// 设置 Content-Type
			ext := filepath.Ext(staticPath)
			switch ext {
			case ".css":
				w.Header().Set("Content-Type", "text/css")
			case ".js":
				w.Header().Set("Content-Type", "application/javascript")
			case ".html":
				w.Header().Set("Content-Type", "text/html")
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			case ".jpg", ".jpeg":
				w.Header().Set("Content-Type", "image/jpeg")
			case ".svg":
				w.Header().Set("Content-Type", "image/svg+xml")
			}
			w.Write(data)
			return
		}
		http.NotFound(w, r)
		return
	}

	// 从嵌入的文件系统读取模板
	tmplData, err := embeddedFS.ReadFile("templates/index.html")
	if err != nil {
		http.Error(w, "模板加载失败", http.StatusInternalServerError)
		return
	}

	// 直接返回 HTML 内容，不使用 template.Parse
	// 因为模板中的 {{ }} 是 Vue.js 语法，与 Go 模板冲突
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(tmplData)
}

// handleGachaLog 分析抽卡数据
func handleGachaLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL    string `json:"url"`
		TaskID string `json:"task_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(Response{Error: "无效的请求数据"})
		return
	}

	// 验证链接
	if valid, msg := api.ValidateGachaURL(req.URL); !valid {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: msg})
		return
	}

	if req.TaskID == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "缺少任务ID"})
		return
	}

	taskMgr.CreateTask(req.TaskID)
	defer taskMgr.DeleteTask(req.TaskID)

	// 从 URL 参数中提取 UID
	params := api.ParseURLQuery(req.URL)
	uid := params["uid"]

	// 获取所有卡池的新数据
	newGachaData := make(map[string][]storage.GachaItem)

	for gachaType := range config.GachaTypes {
		items, ok := gachaAPI.FetchAll(req.URL, gachaType, req.TaskID)
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(Response{Error: "抽卡记录链接已过期！"})
			return
		}
		newGachaData[gachaType] = items
	}

	// 如果 URL 参数中没有 UID，尝试从抽卡数据中提取
	if uid == "" {
		log.Printf("\033[1;33m[DEBUG] URL 参数中没有 UID，尝试从抽卡数据中提取\033[0m")
		for gachaType, items := range newGachaData {
			log.Printf("\033[1;32m[DEBUG] 卡池 %s: 获取到 %d 条记录\033[0m", gachaType, len(items))
			if len(items) > 0 {
				log.Printf("\033[1;32m[DEBUG] 第一条记录: UID=%s, Name=%s\033[0m", items[0].UID, items[0].GetName())
				uid = items[0].UID
				if uid != "" {
					break
				}
			}
		}
	}

	// 如果仍然没有 UID，返回错误
	if uid == "" {
		log.Printf("\033[1;31m[DEBUG] 无法获取 UID，所有卡池数据为空或 UID 字段为空\033[0m")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "无法获取 UID，请确保有抽卡记录"})
		return
	}
	log.Printf("\033[1;32m[DEBUG] 最终获取的 UID: %s\033[0m", uid)

	// 合并数据
	mergedData, newCounts := store.MergeUserData(uid, newGachaData)

	// 保存合并后的数据
	store.SaveUserData(uid, mergedData)

	// 处理数据用于前端展示
	gachaData := make(map[string]PoolData)
	for gachaType := range config.GachaTypes {
		gachaList := mergedData[gachaType]
		gachaData[gachaType] = ProcessGachaData(gachaList, gachaType)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{
		Success:   true,
		Data:      gachaData,
		UID:       uid,
		NewCounts: newCounts,
	})
}

// handleLoadHistory 加载历史记录
func handleLoadHistory(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "缺少 UID 参数"})
		return
	}

	rawData := store.LoadUserData(uid)
	if len(rawData) == 0 {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(Response{Error: "未找到该用户的记录"})
		return
	}

	// 处理数据
	gachaData := make(map[string]PoolData)
	for gachaType := range config.GachaTypes {
		gachaList := rawData[gachaType]
		gachaData[gachaType] = ProcessGachaData(gachaList, gachaType)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{
		Success: true,
		Data:    gachaData,
		UID:     uid,
	})
}

// handleUserList 获取用户列表
func handleUserList(w http.ResponseWriter, r *http.Request) {
	users := store.GetSavedUIDs()

	// 格式化时间
	for i := range users {
		users[i].LastUpdateStr = time.Unix(users[i].LastUpdate, 0).Format("2006-01-02 15:04:05")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{
		Success: true,
		Users:   users,
	})
}

// handleDeleteUser 删除用户
func handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != "DELETE" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	uid := r.URL.Query().Get("uid")
	if uid == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "缺少 UID 参数"})
		return
	}

	if store.DeleteUserData(uid) {
		json.NewEncoder(w).Encode(Response{Success: true, Message: "删除成功"})
	} else {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(Response{Error: "删除失败或用户不存在"})
	}
}

// handleGetProgress 获取进度
func handleGetProgress(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		json.NewEncoder(w).Encode(ProgressResponse{Name: "", Page: "第1页"})
		return
	}

	progress := taskMgr.GetProgress(taskID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ProgressResponse{
		Name: progress.Name,
		Page: progress.Page,
	})
}

// handleExportExcel 导出 Excel
func handleExportExcel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Data map[string]PoolData `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "无效的请求数据"})
		return
	}

	if len(req.Data) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(Response{Error: "无数据可导出"})
		return
	}

	// 创建 Excel 文件
	f := excelize.NewFile()
	defer f.Close()

	// 删除默认 Sheet
	f.DeleteSheet("Sheet1")

	// 表头样式
	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
			Color: "#FFFFFF",
		},
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"#4472C4"},
			Pattern: 1,
		},
		Alignment: &excelize.Alignment{
			Horizontal: "center",
			Vertical:   "center",
		},
	})

	// 正文样式
	bodyStyle, _ := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{
			Horizontal: "left",
			Vertical:   "center",
		},
	})

	// 五星背景色
	fiveStarStyle, _ := f.NewStyle(&excelize.Style{
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"FF4500"},
			Pattern: 1,
		},
	})

	// 四星背景色
	fourStarStyle, _ := f.NewStyle(&excelize.Style{
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"E2EFDA"},
			Pattern: 1,
		},
	})

	headers := []string{"序号", "物品名称", "物品类型", "稀有度", "获取时间", "卡池类型"}

	for gachaType, poolData := range req.Data {
		poolName := poolData.Name
		if poolName == "" {
			poolName = config.GetGachaTypeName(gachaType)
		}

		// 创建 Sheet（名称最长 31 字符）
		if len(poolName) > 31 {
			poolName = poolName[:31]
		}

		sheetName := poolName
		f.NewSheet(sheetName)

		// 写入表头
		for col, header := range headers {
			cell, _ := excelize.CoordinatesToCellName(col+1, 1)
			f.SetCellValue(sheetName, cell, header)
			f.SetCellStyle(sheetName, cell, cell, headerStyle)
		}

		// 写入数据
		for idx, item := range poolData.RawData {
			row := idx + 2

			f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), idx+1)
			f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), item.GetName())
			f.SetCellValue(sheetName, fmt.Sprintf("C%d", row), item.ItemType)
			f.SetCellValue(sheetName, fmt.Sprintf("D%d", row), item.RankType)
			f.SetCellValue(sheetName, fmt.Sprintf("E%d", row), item.Time)
			f.SetCellValue(sheetName, fmt.Sprintf("F%d", row), config.GetGachaTypeName(gachaType))

			// 设置样式
			for col := 1; col <= 6; col++ {
				cell, _ := excelize.CoordinatesToCellName(col, row)
				f.SetCellStyle(sheetName, cell, cell, bodyStyle)

				// 稀有度背景色
				if col == 4 {
					if item.RankType == "5" {
						f.SetCellStyle(sheetName, cell, cell, fiveStarStyle)
					} else if item.RankType == "4" {
						f.SetCellStyle(sheetName, cell, cell, fourStarStyle)
					}
				}
			}
		}

		// 设置列宽
		f.SetColWidth(sheetName, "A", "A", 8)
		f.SetColWidth(sheetName, "B", "B", 20)
		f.SetColWidth(sheetName, "C", "C", 12)
		f.SetColWidth(sheetName, "D", "D", 10)
		f.SetColWidth(sheetName, "E", "E", 22)
		f.SetColWidth(sheetName, "F", "F", 15)

		// 冻结首行
		f.SetPanes(sheetName, &excelize.Panes{
			Freeze:      true,
			Split:       false,
			XSplit:      0,
			YSplit:      1,
			TopLeftCell: "A2",
			ActivePane:  "bottomLeft",
		})
	}

	// 写入缓冲区
	buf := new(bytes.Buffer)
	if err := f.Write(buf); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(Response{Error: "生成 Excel 失败"})
		return
	}

	// 设置响应头
	filename := fmt.Sprintf("原神抽卡记录_%s.xlsx", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))

	// 写入响应
	io.Copy(w, buf)
}

// handleUpdateStatus 获取更新状态
func handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	status := update.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// serveStatic 静态文件服务
func serveStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// 处理静态文件路径
	if strings.HasPrefix(path, "/static/") {
		staticPath := strings.TrimPrefix(path, "/")
		data, err := embeddedFS.ReadFile(staticPath)
		if err == nil {
			// 设置 Content-Type
			ext := filepath.Ext(staticPath)
			switch ext {
			case ".css":
				w.Header().Set("Content-Type", "text/css")
			case ".js":
				w.Header().Set("Content-Type", "application/javascript")
			case ".html":
				w.Header().Set("Content-Type", "text/html")
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			case ".jpg", ".jpeg":
				w.Header().Set("Content-Type", "image/jpeg")
			case ".svg":
				w.Header().Set("Content-Type", "image/svg+xml")
			}
			w.Write(data)
			return
		}
	}

	http.NotFound(w, r)
}

func main() {
	// 远程许可证验证
	license.CheckAndExit()

	// 启动后台定时验证（每15秒检测一次）
	license.StartBackgroundCheck()

	// 启动后台更新检查（每15秒检测一次）
	update.StartBackgroundChecker()

	// 监听系统信号，优雅关闭
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("\033[1;33m正在关闭服务...\033[0m")
		update.StopBackgroundChecker()
		os.Exit(0)
	}()

	// 初始化配置
	cfg := config.GetConfig()

	// 初始化
	store = storage.NewStorage()
	taskMgr = api.NewTaskManager()
	avatarCache = NewAvatarCache()
	gachaAPI = api.NewGachaAPI(taskMgr)

	// 异步加载头像缓存
	avatarCache.LoadAsync()

	// 设置路由
	http.HandleFunc("/", logMiddleware(handleIndex))
	http.HandleFunc("/api/gachaLog", logMiddleware(corsMiddleware(handleGachaLog)))
	http.HandleFunc("/api/loadHistory", logMiddleware(corsMiddleware(handleLoadHistory)))
	http.HandleFunc("/api/userList", logMiddleware(corsMiddleware(handleUserList)))
	http.HandleFunc("/api/deleteUser", logMiddleware(corsMiddleware(handleDeleteUser)))
	http.HandleFunc("/api/getPage", logMiddleware(corsMiddleware(handleGetProgress)))
	http.HandleFunc("/api/exportExcel", logMiddleware(corsMiddleware(handleExportExcel)))

	// 更新相关 API
	http.HandleFunc("/api/update/status", logMiddleware(corsMiddleware(handleUpdateStatus)))

	// 静态文件
	http.HandleFunc("/static/", logMiddleware(serveStatic))

	// 启动服务器
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("\033[1;32m服务器启动在 http://%s\033[0m", addr)
	log.Printf("\033[1;33m数据目录: %s\033[0m", store.GetDataDir())

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("\033[1;31m服务器启动失败:", err, "\033[0m")
	}
}
