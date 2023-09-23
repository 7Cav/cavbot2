import os

import discord
from discord import app_commands
import logging

from cogs.server_management import ServerManagement

if not all(os.getenv(var) for var in ["DISCORD_ID", "API_KEY", "OWNER_ID", "BOT_TOKEN"]):
    logging.error("Missing essential environment variables.")
    exit(1)

logging.basicConfig(level=logging.INFO, format='%(asctime)s:%(levelname)s:%(message)s')

# Create Discord intents and enable message content
intents = discord.Intents.all()
intents.message_content = True

# Create a Discord client instance
client = discord.Client(intents=intents)

# Create a command tree for the client
tree = app_commands.CommandTree(client)

# Set the bot's status activity
status = discord.Activity(name="https://7cav.us", type=3)

# Event handler for when the bot is ready
@client.event
async def on_ready():
    owner_id = int(os.getenv("OWNER_ID"))
    discord_id = int(os.getenv("DISCORD_ID"))
    sypolt = await client.fetch_user(owner_id)
    server_management = ServerManagement(client, tree, sypolt)
    # Set the bot's status activity
    await client.change_presence(activity=status)

    # Sync the command tree with the guild
    await server_management.tree.sync(guild=discord.Object(id=discord_id))

    # Print a message to indicate the bot is ready
    logging.info("Bot ready.")


# Retrieve the bot token from the environment variable and run the client
client.run(os.getenv("BOT_TOKEN"))
