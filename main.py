import asyncio
import os

import aiohttp
import discord
from discord import app_commands
import logging
import traceback

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

discord_id = int(os.getenv("DISCORD_ID"))
owner_id = int(os.getenv("OWNER_ID"))


# Define a dictionary mapping server names to their corresponding server IDs
servers_dict = {
    "A3 TacR": "d7291177",
    "A3 Training 1": "9fd28d83",
    "A3 Training 2": "6b20f439",
    "A3 Training 3": "7b217461",
    "Sq Training": "7e1451cc",
    "Sq TacR": "0b493e80",
}

# Retrieve the API key from the environment variable
api_key = os.getenv("API_KEY")


# Error handler for command errors
@tree.error
async def on_command_error(
    interaction: discord.Interaction, error: discord.app_commands.AppCommandError
) -> None:
    if isinstance(error, app_commands.errors.MissingAnyRole):
        # Respond with an error message if the user doesn't have the required role
        logging.error(f"Missing role! Error: {error}")
        await interaction.response.send(
            f"You must have one of these roles: `{error.missing_roles}` in order to use this command",
            ephemeral=True,
        )
    else:
        # Forward the error message to the bot owner and respond with a generic error message
        logging.error(f"Sypolt Send! Error: {error}")
        await sypolt.send(
            f"Someone broke your bot in a new and interesting way ```{error}```"
        )
        await interaction.followup.send(
            "You managed to break the bot in a way I didn't expect, good job. If the Cav \
had an Army Bug Finder medal I'd give it to you. Anyway, the error was forwarded to me, Sypolt.R. \
Why don't you try whatever that was again but with less breaking things this time?",
            ephemeral=False,
        )


# Command decorator for the restart_server command
@tree.command(
    name="restart_server",
    description="For authorized persons to restart Arma and Squad servers",
    guild=discord.Object(id=discord_id),
)
@app_commands.describe(servers="Servers to choose from")
@app_commands.choices(
    servers=[
        discord.app_commands.Choice(name="A3 TacR", value=1),
        discord.app_commands.Choice(name="A3 Training 1", value=2),
        discord.app_commands.Choice(name="A3 Training 2", value=3),
        discord.app_commands.Choice(name="A3 Training 3", value=4),
        discord.app_commands.Choice(name="Sq Training", value=5),
        discord.app_commands.Choice(name="Sq TacR", value=6),
    ]
)
async def restart_server(interaction, servers: discord.app_commands.Choice[int]):
    # Defer the response to acknowledge the command
    await interaction.response.defer()
    username = interaction.user.name
    logging.info(f"{username} initiated the restart_server command.")

    # Get the server ID based on the chosen server name
    server_id = servers_dict[servers.name]

    # Prepare the API request URL and headers
    url = f"https://panel.7cav.us/api/client/servers/{server_id}/power"
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Accept": "application/json",
        "Content-Type": "application/json",
    }

    # Prepare payload to kill the server process
    payload_kill = '{"signal": "kill"}'

    # Prepare payload to start the server process
    payload_start = '{"signal": "start"}'

    # Send a request to the server
    async with aiohttp.ClientSession() as session:
        try:
            async with session.post(url, headers=headers, data=payload_kill) as response:
                if response.status == 204:
                    await asyncio.sleep(5)

                    async with session.post(url, headers=headers, data=payload_start) as response:
                        if response.status == 204:
                            logging.info(f"Server {servers.name} restarted successfully!")
                            await interaction.followup.send(f"✅ Server {servers.name} restarted successfully!")
                            await sypolt.send(f"DEBUG: {username} used /restart_server and {servers.name} restarted successfully!")
                        else:
                            logging.error(f"Server start failed on {servers.name}.")
                            await interaction.followup.send(f"❌ Server start failed on {servers.name}.")
                            await sypolt.send("DEBUG:{username} used /restart_server and server start failed on {servers.name}.")
                else:
                    logging.error(f"Failed to stop server {servers.name}.")
                    await interaction.followup.send(f"❌ Failed to stop server {servers.name}.")
                    await sypolt.send(f"DEBUG: {username} used /restart_server and failed to stop server {servers.name}.")
        except aiohttp.ClientError as e:
            logging.error(f"Network error: {e}")
            await sypolt.send(f"DEBUG: {username} used /restart_server and network error: {e}")
            await interaction.followup.send(f"❌ Network error: {e}")
        except asyncio.TimeoutError:
            logging.error("API request timed out.")
            await sypolt.send(f"DEBUG: {username} used /restart_server and API request timed out.")
            await interaction.followup.send("❌ API request timed out.")
        except Exception as e:
            logging.error(f"An unexpected error occurred: {e}")
            await interaction.followup.send(f"❌ An unexpected error occurred: {e}")
            error_info = f"Traceback: {traceback.format_exc()}, User: {interaction.user.id}"
            await sypolt.send(f"An unexpected error occurred: {e}, {error_info}")


# Event handler for when the bot is ready
@client.event
async def on_ready():
    global sypolt
    sypolt = await client.fetch_user(owner_id)
    # Set the bot's status activity
    await client.change_presence(activity=status)

    # Sync the command tree with the guild
    await tree.sync(guild=discord.Object(id=discord_id))

    # Print a message to indicate the bot is ready
    logging.info("Bot ready.")


# Retrieve the bot token from the environment variable and run the client
client.run(os.getenv("BOT_TOKEN"))
