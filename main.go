package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "os/signal"
    "strings"
    "sync"
    "syscall"
    "time"

    "github.com/bwmarrin/discordgo"
    "github.com/redis/go-redis/v9"
)


var (
    botToken = os.Getenv("BOT_TOKEN")
    ctx = context.Background()
    rdb *redis.Client
    dg *discordgo.Session
)

type voiceChannelData struct {
    GuildID        string
    ChannelID      string
    CancelFunction context.CancelFunc
}

var managedVoiceChannels = struct {
    sync.RWMutex
    data map[string]*voiceChannelData
}{data: make(map[string]*voiceChannelData)}

const inactivityDuration = 3 * time.Minute

type VoiceChannelInfo struct {
    GuildID   string `json:"guild_id"`
    ChannelID string `json:"channel_id"`
}

func main() {
    if botToken == "" {
        log.Fatalf("BOT_TOKEN environment variable is not set")
    }

    initRedis()

    var err error
    dg, err = discordgo.New(botToken)
    if err != nil {
        log.Fatalf("Error creating Discord session: %v", err)
    }

    // 3) Open the Discord session
    err = dg.Open()
    if err != nil {
        log.Fatalf("Cannot open the Discord session: %v", err)
    }
    defer dg.Close()

    restoreManagedChannels()

    dg.AddHandler(onInteractionCreate)
    dg.AddHandler(onVoiceStateUpdate)

    guildID := ""
    _, err = dg.ApplicationCommandCreate(dg.State.User.ID, guildID, &discordgo.ApplicationCommand{
        Name:        "createvoice",
        Description: "Create a temporary voice channel with optional user limit & custom name.",
        Options: []*discordgo.ApplicationCommandOption{
            {
                Type:        discordgo.ApplicationCommandOptionString,
                Name:        "channelname",
                Description: "Custom name for the voice channel (up to 100 chars).",
                Required:    false,
            },
            {
                Type:        discordgo.ApplicationCommandOptionInteger,
                Name:        "userlimit",
                Description: "Max number of users (0 = no limit).",
                Required:    false,
            },
        },
    })
    if err != nil {
        log.Printf("Cannot create 'createvoice' command: %v", err)
    }

    _, err = dg.ApplicationCommandCreate(dg.State.User.ID, guildID, &discordgo.ApplicationCommand{
        Name:        "help",
        Description: "Show usage info and available commands.",
    })
    if err != nil {
        log.Printf("Cannot create 'help' command: %v", err)
    }

    stop := make(chan os.Signal, 1)
    signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
    <-stop

    log.Println("Shutting down bot...")

    func() {
        managedVoiceChannels.Lock()
        defer managedVoiceChannels.Unlock()
        for _, v := range managedVoiceChannels.data {
            v.CancelFunction()
        }
    }()

    dg.Close()
    log.Println("Bot shut down cleanly.")
}

func initRedis() {
    redisURL := os.Getenv("REDIS_URL")
    if redisURL != "" {
        opt, err := redis.ParseURL(redisURL)
        if err != nil {
            log.Fatalf("Failed to parse REDIS_URL: %v", err)
        }
        rdb = redis.NewClient(opt)
    } else {
        redisAddr := os.Getenv("REDIS_HOST")
        if redisAddr == "" {
            redisAddr = "localhost:6379"
        }

        redisPassword := os.Getenv("REDIS_PASSWORD")
        redisDB := 0
        if dbStr := os.Getenv("REDIS_DB"); dbStr != "" {
            fmt.Sscanf(dbStr, "%d", &redisDB)
        }

        rdb = redis.NewClient(&redis.Options{
            Addr:     redisAddr,
            Password: redisPassword,
            DB:       redisDB,
        })
    }

    if err := rdb.Ping(ctx).Err(); err != nil {
        log.Fatalf("Failed to connect to Redis: %v", err)
    }

    log.Println("Connected to Redis successfully.")
}

func storeChannelInRedis(vc VoiceChannelInfo) error {
    data, err := json.Marshal(vc)
    if err != nil {
        return err
    }

    return rdb.HSet(ctx, "tmpVoiceChannels", vc.ChannelID, data).Err()
}


func removeChannelFromRedis(channelID string) error {
    return rdb.HDel(ctx, "tmpVoiceChannels", channelID).Err()
}

func restoreManagedChannels() {
    entries, err := rdb.HGetAll(ctx, "tmpVoiceChannels").Result()
    if err != nil {
        log.Printf("[Error] Could not HGetAll from Redis: %v", err)
        return
    }

    for channelID, rawJSON := range entries {
        var info VoiceChannelInfo
        if err := json.Unmarshal([]byte(rawJSON), &info); err != nil {
            log.Printf("[Warning] Could not parse channel info for %s: %v", channelID, err)
            continue
        }

        // Check if the channel still exists in Discord's state
        ch, err := dg.State.Channel(info.ChannelID)
        if err != nil || ch == nil {
            // If it doesn't exist, remove from Redis
            _ = removeChannelFromRedis(channelID)
            continue
        }

        // Otherwise, re-launch a watcher
        ctxWatch, cancel := context.WithCancel(context.Background())
        managedVoiceChannels.Lock()
        managedVoiceChannels.data[info.ChannelID] = &voiceChannelData{
            GuildID:        info.GuildID,
            ChannelID:      info.ChannelID,
            CancelFunction: cancel,
        }
        managedVoiceChannels.Unlock()

        go watchChannelInactivity(ctxWatch, dg, info.ChannelID, info.GuildID)
        log.Printf("[Restore] Re-launched watcher for channel %s", channelID)
    }
}

func onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
    switch i.Type {
    case discordgo.InteractionPing:
        s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
            Type: discordgo.InteractionResponsePong,
        })

    case discordgo.InteractionApplicationCommand:
        data := i.ApplicationCommandData()
        switch data.Name {
        case "createvoice":
            handleCreateVoice(s, i)
        case "help":
            handleHelp(s, i)
        default:
            log.Printf("[Info] Unrecognized command: %s", data.Name)
        }
    }
}

func handleCreateVoice(s *discordgo.Session, i *discordgo.InteractionCreate) {
    data := i.ApplicationCommandData()
    userLimit := 0
    channelName := fmt.Sprintf("VoiceManager %d", time.Now().Unix())

    // parse options
    for _, opt := range data.Options {
        switch opt.Name {
        case "channelname":
            if val, ok := opt.Value.(string); ok {
                val = strings.TrimSpace(val)
                if len(val) > 100 {
                    val = val[:100]
                }
                if len(val) > 0 {
                    channelName = val
                }
            }
        case "userlimit":
            if val, ok := opt.Value.(float64); ok {
                if val < 0 {
                    val = 0
                }
                userLimit = int(val)
            }
        }
    }

    // optionally find a parent category that has "voice" in its name
    var parentCategoryID string
    channels, err := s.GuildChannels(i.GuildID)
    if err == nil {
        for _, ch := range channels {
            if ch.Type == discordgo.ChannelTypeGuildCategory && strings.Contains(strings.ToLower(ch.Name), "voice") {
                parentCategoryID = ch.ID
                break
            }
        }
    }

    // create the voice channel
    channel, err := s.GuildChannelCreateComplex(i.GuildID, discordgo.GuildChannelCreateData{
        Name:      channelName,
        Type:      discordgo.ChannelTypeGuildVoice,
        UserLimit: userLimit,
        ParentID:  parentCategoryID,
    })
    if err != nil {
        respondError(s, i, fmt.Sprintf("Failed to create channel: %v", err))
        return
    }

    respondMessage(s, i, fmt.Sprintf("Created voice channel <#%s> with name **%s** and max user limit %d.",
        channel.ID, channel.Name, userLimit))

    ctxWatch, cancel := context.WithCancel(context.Background())
    managedVoiceChannels.Lock()
    managedVoiceChannels.data[channel.ID] = &voiceChannelData{
        GuildID:        i.GuildID,
        ChannelID:      channel.ID,
        CancelFunction: cancel,
    }
    managedVoiceChannels.Unlock()

    _ = storeChannelInRedis(VoiceChannelInfo{
        GuildID:   i.GuildID,
        ChannelID: channel.ID,
    })

    go watchChannelInactivity(ctxWatch, s, channel.ID, i.GuildID)
}

// handleHelp is invoked by the /help command
func handleHelp(s *discordgo.Session, i *discordgo.InteractionCreate) {
    msg := "**Available Commands**:\n\n" +
        "**/createvoice** – Create a temporary voice channel.\n" +
        "`channelname` (optional): Name for the channel\n" +
        "`userlimit` (optional): Limit of users (0 = no limit)\n\n" +
        "**/help** – Show this help message."

    respondEphemeral(s, i, msg)
}

func onVoiceStateUpdate(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
    managedVoiceChannels.RLock()
    _, exists := managedVoiceChannels.data[v.ChannelID]
    managedVoiceChannels.RUnlock()

    if !exists {
        return
    }
}


func watchChannelInactivity(ctx context.Context, s *discordgo.Session, channelID, guildID string) {
    defer func() {
        if r := recover(); r != nil {
            log.Printf("[Recovery] watchChannelInactivity recovered from panic: %v", r)
        }
    }()

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            ch, err := s.State.Channel(channelID)
            if err != nil || ch == nil {
                log.Printf("[Info] Channel %s no longer exists or cannot be accessed; cleaning up.", channelID)
                cleanUpChannel(channelID)
                return
            }

            empty, err := isVoiceChannelEmpty(s, guildID, channelID)
            if err != nil {
                log.Printf("[Warning] Could not check occupancy for channel %s: %v", channelID, err)
                continue
            }

            if empty {
                if confirmEmptyAfterDelay(ctx, s, guildID, channelID, inactivityDuration) {
                    log.Printf("[Info] Channel %s was empty for %v. Deleting...", channelID, inactivityDuration)
                    err = deleteChannelSafely(s, channelID)
                    if err != nil {
                        log.Printf("[Error] Could not delete channel %s: %v", channelID, err)
                    }
                    cleanUpChannel(channelID)
                    return
                }
            }
        }
    }
}

func confirmEmptyAfterDelay(ctx context.Context, s *discordgo.Session, guildID, channelID string, d time.Duration) bool {
    timer := time.NewTimer(d)
    defer timer.Stop()

    select {
    case <-ctx.Done():
        return false
    case <-timer.C:
        empty, err := isVoiceChannelEmpty(s, guildID, channelID)
        if err != nil {
            log.Printf("[Warning] confirmEmptyAfterDelay: could not re-check occupancy: %v", err)
            return false
        }
        return empty
    }
}

func isVoiceChannelEmpty(s *discordgo.Session, guildID, channelID string) (bool, error) {
    guild, err := s.State.Guild(guildID)
    if err != nil {
        return false, err
    }
    for _, vs := range guild.VoiceStates {
        if vs.ChannelID == channelID {
            return false, nil
        }
    }
    return true, nil
}

func deleteChannelSafely(s *discordgo.Session, channelID string) error {
    _, err := s.ChannelDelete(channelID)
    if err == nil {
        // If we succeed, remove from Redis
        _ = removeChannelFromRedis(channelID)
    }
    return err
}

func cleanUpChannel(channelID string) {
    managedVoiceChannels.Lock()
    defer managedVoiceChannels.Unlock()

    if data, ok := managedVoiceChannels.data[channelID]; ok {
        data.CancelFunction()
        delete(managedVoiceChannels.data, channelID)
    }
    // Also ensure we remove from Redis in case it wasn't removed already
    _ = removeChannelFromRedis(channelID)
}

func respondMessage(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
    err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: message,
        },
    })
    if err != nil {
        log.Printf("[Error] respondMessage: cannot respond to interaction: %v", err)
    }
}

func respondError(s *discordgo.Session, i *discordgo.InteractionCreate, errMsg string) {
    respondMessage(s, i, "Error: "+errMsg)
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, message string) {
    err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: message,
            Flags:   1 << 6,
        },
    })
    if err != nil {
        log.Printf("[Error] respondEphemeral: cannot respond to interaction: %v", err)
    }
}
