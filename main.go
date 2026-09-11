package main

import (
	"context"
	"fmt"
	"github.com/fiatjaf/eventstore/postgresql"
	"github.com/fiatjaf/khatru"
	"github.com/nbd-wtf/go-nostr"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	onlineUsers = make(map[string]time.Time)
	mu          sync.Mutex
)

// 更新在线状态的辅助函数
func markUserActive(pubkey string, action string) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	threshold := 30 * time.Minute // 超过 30 分钟没活动就清理
	for pubkey, lastActive := range onlineUsers {
		if now.Sub(lastActive) > threshold {
			delete(onlineUsers, pubkey)
		}
	}
	onlineUsers[pubkey] = time.Now()
}

// 打印并获取当前在线详情
func getOnlineStatus() (int, []string) {
	mu.Lock()
	defer mu.Unlock()

	count := len(onlineUsers)
	list := make([]string, 0, count)
	for pk := range onlineUsers {
		// 取前 8 位显示即可，保护隐私也方便阅读
		list = append(list, pk[:8])
	}
	return count, list
}

func main() {
	relay := khatru.NewRelay()

	// 基本信息配置
	relay.Info.Name = "Helios Relay"
	relay.Info.PubKey = "b81e6789a2c789751125c1e7872c30e9f7bcc4c19faebe9f948bc8d8ec680d45"
	relay.Info.Description = "海米 Nostr 中继站点 (测试)"
	relay.Info.Version = "2026.1.15.1"
	relay.Info.Icon = "https://sun.29t.com/icons/app-icon.png"
	// Banner 为 NIP-11 可选字段，新品牌素材就位后再填（旧地址已失效，留空好过死链）
	// relay.Info.Banner = "https://sun.29t.com/..."
	relay.Info.Software = "https://github.com/fiatjaf/khatru"
	relay.Info.Contact = "mailto:45online@gmail.com"
	// relay.Info.NIPs = []int{1, 2, 9, 11, 12, 15, 16, 20, 22, 33, 40, 42, 50}

	relay.OnConnect = append(relay.OnConnect, func(ctx context.Context) {
		fmt.Printf("[%s] [INFO] 新客户端连接已建立\n", time.Now().Format("15:04:05"))
	})
	//https://nostr.watch/relays/wss/relay.29t.com

	// 数据库连接必须由环境变量提供，不设默认值（避免把凭证写进仓库）
	//   例: DATABASE_URL=postgresql://postgres:<password>@127.0.0.1:5432/relay_db?sslmode=disable
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		panic("需要设置环境变量 DATABASE_URL")
	}

	db := postgresql.PostgresBackend{DatabaseURL: dsn}
	if err := db.Init(); err != nil {
		panic(fmt.Sprintf("Postgres 初始化失败: %v", err))
	}

	// 绑定存储接口
	relay.StoreEvent = append(relay.StoreEvent, db.SaveEvent)
	relay.QueryEvents = append(relay.QueryEvents, db.QueryEvents)
	relay.CountEvents = append(relay.CountEvents, db.CountEvents)
	relay.DeleteEvent = append(relay.DeleteEvent, db.DeleteEvent)
	relay.ReplaceEvent = append(relay.ReplaceEvent, db.ReplaceEvent)

	//https://nostr.how/zh/the-protocol
	//https://cloud.tencent.com/developer/mcp/server/11553
	//https://github.com/nostr-protocol/nips
	var allowedKinds = map[int]bool{
		// --- 基础微博与资料 ---
		0:  true, // Metadata: 用户头像、昵称、简介
		1:  true, // Short Text Note: 微博正文、评论、回复
		3:  true, // Contact List: 关注列表（用于计算粉丝数和关注流）
		5:  true, // 删除事件
		6:  true, // Repost: 转发帖子
		7:  true, // Reaction: 点赞、表情回应
		16: true, // Generic Repost: 转发非 Kind 1 的内容（如转发一篇文章）

		// --- 进阶互动与打赏 ---
		9:    true, // Event Deletion: 用户删除自己帖子的声明
		9735: true, // Zap: 收到打赏的收据（微博打赏功能核心）

		// --- 私信与即时通讯 (IM) ---
		4:    true, // Encrypted DM: 传统加密私信 (NIP-04)
		14:   true, // Direct Message: 现代密封聊天 (NIP-17)，更安全
		1059: true, // Gift Wrap: 消息信封，用于彻底隐藏私信元数据
		40:   true, // Channel Creation: 创建群聊/社区
		41:   true, // Channel Metadata: 修改群信息（头像、公告）
		42:   true, // Channel Message: 群聊/超话内的发言

		// --- 社区、超话与治理 (NIP-172) ---
		34550: true, // Community Definition: 定义一个“社区”或“超话”
		34551: true, // Community Post Approval: 社区管理员批准发帖（用于审核制社区）

		// --- 内容存储与长文 ---
		30023: true, // Long-form Content: 长文章（类似头条/博客）
		1063:  true, // File Metadata: 图片、视频的文件元数据（极简文件柜）

		// --- 用户偏好与个性化 (你要求的) ---
		5300:  true, // Live Tracking: 实时路径、足迹分享
		10015: true, // Interests: 兴趣标签（用于 Spring Boot 给用户推送感兴趣的内容）
		30078: true, // Application-Specific Data: 存草稿、App 自定义配置（最万能的扩展位）

		// --- 列表与路由管理 ---
		10000: true, // Mute List: 拉黑名单
		10002: true, // Relay List Metadata: 告诉别人该去哪个中继找我（必备）
		30000: true, // Categorized People List: 关注分组（如“家人”、“同事”）
		10012: true, // Generic Lists: 通用收藏夹、自定义列表
	}
	// 过滤器逻辑
	relay.RejectEvent = append(relay.RejectEvent, func(ctx context.Context, event *nostr.Event) (bool, string) {
		// if event.Kind != 1 && event.Kind != 0 {
		// 	fmt.Printf("[%s] [REJECT] 拒绝非核心事件 Kind: %d, ID: %s\n", time.Now().Format("15:04:05"), event.Kind, event.ID)
		// 	return true, "only kind 0 and 1"
		// }

		// --- 状态处理逻辑 ---
		markUserActive(event.PubKey, fmt.Sprintf("Kind %d", event.Kind))

		if event.Kind == 30315 || event.Kind == 22456 { //22456是0xchat的心跳(加密)
			// NIP-315 约定内容为空或特定字符表示在线，或者看 expiration
			if event.Content == "offline" {
				mu.Lock()
				delete(onlineUsers, event.PubKey)
				mu.Unlock()
				//fmt.Printf("📢 用户主动离线: %s\n", event.PubKey)
			} else {
				//markUserActive(event.PubKey, "Kind 30315 上线")
				//fmt.Printf("[%s] [STATUS] 🟢 用户上线: %s, 状态: %s\n", time.Now().Format("15:04:05"), event.PubKey, event.Content)
			}
		}
		count, pubkeyList := getOnlineStatus()
		if !allowedKinds[event.Kind] {
			fmt.Printf("[%s] [REJECT] 拒绝非核心事件 Kind: %d, PubKey: %s\n%d:%s\n%s\n", time.Now().Format("15:04:05"), event.Kind, event.PubKey, count, pubkeyList, event.Content)
			return true, fmt.Sprintf("Kind %d not supported", event.Kind)
		}
		// 3. 针对帖子 (Kind 1,6,7,30023) 的专属 App 过滤
		if event.Kind == 0 || event.Kind == 1 || event.Kind == 4 || event.Kind == 5 || event.Kind == 6 || event.Kind == 7 || event.Kind == 10000 || event.Kind == 10002 || event.Kind == 30023 || event.Kind == 30078 || event.Kind == 9375 {
			hasAppTag := false
			if len(event.Tags) > 0 {
				fmt.Printf("[%s] [TAGS] : %s\n", time.Now().Format("15:04:05"), event.Tags)
			}
			for _, tag := range event.Tags {
				// 标签名 "t"：helios 为当前值；injapan / 29t 是历史 tag，保留以兼容存量客户端
				if len(tag) >= 2 && tag[0] == "t" && (tag[1] == "helios" || tag[1] == "injapan" || tag[1] == "29t") {
					hasAppTag = true
					break
				}
			}
			if !hasAppTag {
				fmt.Printf("[%s] [BLOCKED] 拒绝外来帖子 PubKey: %s (缺少 ['t', 'helios'] 标签)\n%d:%s\n%s\n",
					time.Now().Format("15:04:05"), event.PubKey, count, pubkeyList, event.Content)
				return true, "this relay only accepts posts from Helios App"
			}
		}
		fmt.Printf("[%s] [SUCCESS] 允许存储事件: %d PubKey: %s\n%s\n", time.Now().Format("15:04:05"), event.Kind, event.PubKey, event.Content)
		return false, ""
	})

	fmt.Printf(">>> 2026 Helios Relay 系统启动成功 <<<\n")
	fmt.Printf("运行模式: PostgreSQL 模式\n")
	fmt.Printf("监听端口: 8383\n")
	fmt.Println("-------------------------------------------")
	http.ListenAndServe(":8383", relay)
}
