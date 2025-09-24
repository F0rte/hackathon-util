package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
}

func getTeamData(spreadsheetID, rangeStr, credentialsFile string) ([][]interface{}, error) {
	srv, err := sheets.NewService(context.Background(), option.WithCredentialsFile(credentialsFile))
	if err != nil {
		return nil, err
	}

	resp, err := srv.Spreadsheets.Values.Get(spreadsheetID, rangeStr).Do()
	if err != nil {
		return nil, err
	}

	return resp.Values, nil
}

func buildUsernameToIDMap(dg *discordgo.Session, guildID string, max int) (map[string]string, error) {
	userMap := make(map[string]string)
	after := ""
	total := 0

	for total < max {
		members, err := dg.GuildMembers(guildID, after, 1000)
		if err != nil {
			return nil, err
		}
		if len(members) == 0 {
			break
		}

		for _, m := range members {
			if m.User.Username != "" {
				userMap[strings.ToLower(m.User.Username)] = m.User.ID
			}
			after = m.User.ID
			total++
		}
	}

	return userMap, nil
}

func createParticipantsRole(dg *discordgo.Session, guildID, eventName string, existingRoles map[string]string, mentionable bool) (string, string, error) {
	participantsRoleName := "参加者_" + eventName
	mentorRoleName := "メンター_" + eventName

	var paricipantsRoleID, mentorRoleID string
	// @参加者_{ハッカソン名}
	paricipantsRoleID, p_exists := existingRoles[participantsRoleName]
	if !p_exists {
		// ロールが存在しない場合は作成
		role, err := dg.GuildRoleCreate(guildID, &discordgo.RoleParams{
			Name:        participantsRoleName,
			Mentionable: &mentionable,
		})
		if err != nil {
			log.Printf("[ERROR] Failed to create participants role '%s': %v", participantsRoleName, err)
		} else {
			paricipantsRoleID = role.ID
			log.Printf("[OK] participants role created: %s", participantsRoleName)
		}
	} else {
		log.Printf("[SKIP] participants role already exists: %s", participantsRoleName)
	}

	// @メンター_{ハッカソン名}
	mentorRoleID, m_exists := existingRoles[mentorRoleName]
	if !m_exists {
		// ロールが存在しない場合は作成
		role, err := dg.GuildRoleCreate(guildID, &discordgo.RoleParams{
			Name:        mentorRoleName,
			Mentionable: &mentionable,
		})
		if err != nil {
			log.Printf("[ERROR] Failed to create mentor role '%s': %v", mentorRoleName, err)
		} else {
			mentorRoleID = role.ID
			log.Printf("[OK] mentor role created: %s", mentorRoleName)
		}
	} else {
		log.Printf("[SKIP] mentor role already exists: %s", mentorRoleName)
	}

	return paricipantsRoleID, mentorRoleID, nil
}

func buildPermissionOverwrites(paricipantsRoleID, mentorRoleID, guildID string) []*discordgo.PermissionOverwrite {
	overwrites := []*discordgo.PermissionOverwrite{
		{
			// @everyone
			ID: guildID,
			Type: discordgo.PermissionOverwriteTypeRole,
			Deny: discordgo.PermissionViewChannel,
			Allow: 0,
		},
		{
			// @ハッカソン参加者
			ID: paricipantsRoleID,
			Type: discordgo.PermissionOverwriteTypeRole,
			Deny: 0,
			Allow: discordgo.PermissionViewChannel,
		},
		{
			// @ハッカソンメンター
			ID: mentorRoleID,
			Type: discordgo.PermissionOverwriteTypeRole,
			Deny: 0,
			Allow: discordgo.PermissionViewChannel,
		},
	}
	return overwrites
}

func main() {
	loadEnv()

	spreadsheetID := os.Getenv("GOOGLE_SPREADSHEET_ID")
	botToken := os.Getenv("DISCORD_BOT_TOKEN")
	guildID := os.Getenv("DISCORD_GUILD_ID")
	credentialsFile := os.Getenv("GOOGLE_CREDENTIALS_FILE")
	teamRange := os.Getenv("TEAM_RANGE")
	eventName := os.Getenv("EVENT_NAME")

	if spreadsheetID == "" || botToken == "" || guildID == "" || credentialsFile == "" || teamRange == "" || eventName == "" {
		log.Fatal("One or more required environment variables are not set.")
	}
	notFoundUsers := []string{} // ← 追加：見つからなかったユーザー一覧
	teamData, err := getTeamData(spreadsheetID, teamRange, credentialsFile)
	if err != nil {
		log.Fatalf("Failed to fetch team data: %v", err)
	}

	dg, err := discordgo.New("Bot " + botToken)
	if err != nil {
		log.Fatalf("Failed to create Discord session: %v", err)
	}
	defer dg.Close()

	// 既存ロール
	roles, err := dg.GuildRoles(guildID)
	if err != nil {
		log.Fatalf("Failed to fetch roles: %v", err)
	}
	existingRoles := make(map[string]string)
	for _, r := range roles {
		existingRoles[r.Name] = r.ID
	}

	// 既存カテゴリ
	channels, err := dg.GuildChannels(guildID)
	if err != nil {
		log.Fatalf("Failed to fetch channels: %v", err)
	}
	existingCategories := make(map[string]string)
	for _, ch := range channels {
		if ch.Type == discordgo.ChannelTypeGuildCategory {
			existingCategories[ch.Name] = ch.ID
		}
	}

	// 参加者ロール・メンターロール作成
	mentionable := true
	participantsRoleId, mentorRoleId, _ := createParticipantsRole(dg, guildID, eventName, existingRoles, mentionable)

	// チャンネルの権限設定
	overwrites := buildPermissionOverwrites(participantsRoleId, mentorRoleId, guildID)

	// 各チーム処理
	for _, row := range teamData {
		if len(row) == 0 {
			continue
		}
		teamName := fmt.Sprintf("%v", row[0])
		if teamName == "" {
			continue
		}

		var roleID string

		// ロール作成または取得
		if id, exists := existingRoles[teamName]; exists {
			roleID = id
			log.Printf("[SKIP] Role already exists: %s", teamName)
		} else {
			role, err := dg.GuildRoleCreate(guildID, &discordgo.RoleParams{
				Name:        teamName,
				Mentionable: &mentionable,
			})
			if err != nil {
				log.Printf("[ERROR] Role create: %s - %v", teamName, err)
				continue
			}
			roleID = role.ID
			existingRoles[teamName] = roleID
			log.Printf("[OK] Role created: %s", teamName)
		}

		// カテゴリ作成または取得
		var categoryID string
		if id, exists := existingCategories[teamName]; exists {
			categoryID = id
			log.Printf("[SKIP] Category already exists: %s", teamName)
		} else {
			category, err := dg.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name: teamName,
				Type: discordgo.ChannelTypeGuildCategory,
			})
			if err != nil {
				log.Printf("[ERROR] Category create: %s - %v", teamName, err)
				continue
			}
			categoryID = category.ID
			existingCategories[teamName] = categoryID
			log.Printf("[OK] Category created: %s", teamName)

			_, err = dg.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name:     "やりとり",
				Type:     discordgo.ChannelTypeGuildText,
				ParentID: categoryID,
			})
			if err != nil {
				log.Printf("[ERROR] Text channel create: %s - %v", teamName, err)
			}

			_, err = dg.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name:     "会話",
				Type:     discordgo.ChannelTypeGuildVoice,
				ParentID: categoryID,
				PermissionOverwrites: overwrites,
			})
			if err != nil {
				log.Printf("[ERROR] Voice channel create: %s - %v", teamName, err)
			}
		}

		userMap, err := buildUsernameToIDMap(dg, guildID, 3000)
		if err != nil {
			log.Fatalf("Failed to fetch guild members: %v", err)
		}

		// メンバーにロール付与（B〜F列）
		for i := 1; i <= 5; i++ {
			var rawUsername string
			if i < len(row) {
				rawUsername = fmt.Sprintf("%v", row[i])
			} else {
				rawUsername = ""
			}
			username := strings.ToLower(strings.TrimSpace(rawUsername))

			if username == "" {
				continue
			}

			userID, ok := userMap[username]
			if !ok {
				log.Printf("[SKIP] Username not found in guild: %s (%s)", username, teamName)
				continue
			}

			member, err := dg.GuildMember(guildID, userID)
			if err != nil || member == nil {
				log.Printf("[SKIP] Could not retrieve member: %s (%s)", username, teamName)
				continue
			}

			// ロール重複チェック
			hasRole := false
			for _, r := range member.Roles {
				if r == roleID {
					hasRole = true
					break
				}
			}
			if hasRole {
				log.Printf("[SKIP] %s already has role '%s'", username, teamName)
				continue
			}

			err = dg.GuildMemberRoleAdd(guildID, userID, roleID)
			if err != nil {
				log.Printf("[ERROR] Failed to assign role '%s' to %s: %v", teamName, username, err)
			} else {
				log.Printf("[OK] Assigned role '%s' to %s", teamName, username)
			}
		}

		for i := 1; i <= 5; i++ {
			var rawUsername string
			if i < len(row) {
				rawUsername = fmt.Sprintf("%v", row[i])
			} else {
				continue
			}
			username := strings.ToLower(strings.TrimSpace(rawUsername))
			if username == "" {
				continue
			}

			userID, ok := userMap[username]
			if !ok {
				log.Printf("[SKIP] Username not found for ALL_MEMBERS: %s", username)
				notFoundUsers = append(notFoundUsers, username) // ← 追加
				continue
			}

			member, err := dg.GuildMember(guildID, userID)
			if err != nil || member == nil {
				log.Printf("[SKIP] Could not fetch member for ALL_MEMBERS: %s", username)
				continue
			}

			// 重複チェック
			hasRole := false
			for _, r := range member.Roles {
				if r == participantsRoleId {
					hasRole = true
					break
				}
			}
			if hasRole {
				log.Printf("[SKIP] %s already has ALL_MEMBERS role", username)
				continue
			}

			err = dg.GuildMemberRoleAdd(guildID, userID, participantsRoleId)
			if err != nil {
				log.Printf("[ERROR] Failed to assign ALL_MEMBERS role to %s: %v", username, err)
			} else {
				log.Printf("[OK] Assigned ALL_MEMBERS role to %s", username)
			}
		}

		fmt.Println("✅ 完了しました")
	}
	if len(notFoundUsers) > 0 {
		fmt.Println("🔍 Discordで見つからなかったユーザー一覧:")
		for _, name := range notFoundUsers {
			fmt.Println(name)
		}
	}
}
