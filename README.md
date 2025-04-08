# VoiceManager Discord Bot

VoiceManager is a Discord bot written in Go that allows users to create **temporary voice channels** with optional user limits. These channels are automatically deleted after being inactive for a certain amount of time.

## 🚀 Features

- Slash command `/createvoice` to create a custom voice channel.
- Optional channel name and user limit.
- Auto-deletes the channel after **3 minutes of inactivity**.
- Categorizes new channels under any category containing "voice" in its name.
- Slash command `/help` for usage info.

## 🛠 Add VoiceManager to Your Server

Click the link below to invite **VoiceManager** to your Discord server:

👉 [Invite VoiceManager](https://discord.com/oauth2/authorize?client_id=1358850234192761043&scope=bot%20applications.commands&permissions=3145744)


This bot requires the following permissions:

- `Manage Channels` – To create and delete temporary voice channels.
- `View Channels` – To access guild channel info.
- `Connect` and `Speak` – To interact with voice channels.

> Note: Make sure you have the **"Manage Server"** permission in your Discord server to invite the bot.