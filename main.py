import asyncio
import datetime
import os
import typing

import aiohttp
import discord
from discord import app_commands

# Create Discord intents and enable message content
intents = discord.Intents.all()
intents.message_content = True

# Create a Discord client instance
client = discord.Client(intents=intents)

# Create a command tree for the client
tree = app_commands.CommandTree(client)

# Set the bot's status activity
status = discord.Activity(name="https://7cav.us", type=3)

# Define a dictionary mapping server names to their corresponding server IDs
servers_dict = {
    "A3 TacR": "d7291177",
    "A3 Training 1": "9fd28d83",
    "A3 Training 2": "6b20f439",
    "A3 Training 3": "7b217461",
    "SQ Training": "7e1451cc",
}

discordID = os.getenv("DISCORD_ID")
ownerID = os.getenv("OWNER_ID")
# Retrieve the API key from the environment variable
api_key = os.getenv("API_KEY")


# Error handler for command errors
@tree.error
async def on_command_error(
    interaction: discord.Interaction, error: discord.app_commands.AppCommandError
) -> None:
    if isinstance(error, app_commands.errors.MissingAnyRole):
        # Respond with an error message if the user doesn't have the required role
        await interaction.response.send_message(
            f"You must have one of these roles: `{error.missing_roles}` in order to use this command",
            ephemeral=True,
        )
    else:
        # Forward the error message to the bot owner and respond with a generic error message
        sypolt = await client.fetch_user(ownerID)
        await sypolt.send(
            f"Someone broke your bot in a new and interesting way ```{error}```"
        )
        await interaction.response.send_message(
            "You managed to break the bot in a way I didn't expect, good job. If the Cav \
had an Army Bug Finder medal I'd give it to you. Anyway the error was forwarded to me, Sypolt.R. \
Why don't you try whatever that was again but with less breaking things this time?",
            ephemeral=True,
        )


# Command decorator for the restart_server command
@tree.command(
    name="restart_server",
    description="For SGT+ to restart Arma and Squad servers",
    guild=discord.Object(id=discordID),
)
@app_commands.describe(servers="Servers to choose from")
@app_commands.choices(
    servers=[
        discord.app_commands.Choice(name="A3 TacR", value=1),
        discord.app_commands.Choice(name="A3 Training 1", value=2),
        discord.app_commands.Choice(name="A3 Training 2", value=3),
        discord.app_commands.Choice(name="A3 Training 3", value=4),
        discord.app_commands.Choice(name="Sq Training", value=5),
    ]
)
async def restart_server(interaction, servers: discord.app_commands.Choice[int]):
    # Defer the response to acknowledge the command
    await interaction.response.defer()

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

    # Send a request to stop the server
    async with aiohttp.ClientSession() as session:
        async with session.post(url, headers=headers, data=payload_kill) as response:
            if response.status == 204:
                # Wait for a few seconds to ensure the server is stopped
                await asyncio.sleep(5)

                # Send a request to start the server
                async with aiohttp.ClientSession() as session:
                    async with session.post(
                        url, headers=headers, data=payload_start
                    ) as response:
                        if response.status == 204:
                            # Respond with a success message if the server restarted successfully
                            await interaction.followup.send(
                                f"Server {servers.name} restarted successfully!"
                            )
                        else:
                            # Respond with an error message if the server failed to start
                            await interaction.followup.send(
                                f"Server start failed on {servers.name}."
                            )
            else:
                # Respond with an error message if the server failed to stop
                await interaction.followup.send(
                    f"Failed to stop server {servers.name}."
                )


@tree.command(
    name="timestamp_builder",
    description="Helps build discord timestamps to have proper countdowns and such",
    guild=discord.Object(id=discordID),
)
@app_commands.describe(year="Specify the year (defaults to current year)")
@app_commands.describe(month="Specify the month (defaults to current month)")
@app_commands.describe(day="Specify the day (defaults to current day)")
@app_commands.describe(hour="Specify the hour (defaults to current hour)")
@app_commands.describe(minute="Specify the minute (defaults to current minute)")
@app_commands.describe(second="Specify the second (defaults to 0)")
@app_commands.describe(timezone="Specify the timezone (defaults to UTC)")
@app_commands.describe(format="Specify a format (Shows all if not set)")
@app_commands.describe(ephemeral="Set false to allow others to see")
@app_commands.choices(
    timezone=[
        discord.app_commands.Choice(name="UTC-12", value=-12),
        discord.app_commands.Choice(name="UTC-11", value=-11),
        discord.app_commands.Choice(name="UTC-10", value=-10),
        discord.app_commands.Choice(name="UTC-9", value=-9),
        discord.app_commands.Choice(name="UTC-8", value=-8),
        discord.app_commands.Choice(name="UTC-7", value=-7),
        discord.app_commands.Choice(name="UTC-6", value=-6),
        discord.app_commands.Choice(name="UTC-5", value=-5),
        discord.app_commands.Choice(name="UTC-4", value=-4),
        discord.app_commands.Choice(name="UTC-3", value=-3),
        discord.app_commands.Choice(name="UTC-2", value=-2),
        discord.app_commands.Choice(name="UTC-1", value=-1),
        discord.app_commands.Choice(name="UTC+0", value=0),
        discord.app_commands.Choice(name="UTC+1", value=1),
        discord.app_commands.Choice(name="UTC+2", value=2),
        discord.app_commands.Choice(name="UTC+3", value=3),
        discord.app_commands.Choice(name="UTC+4", value=4),
        discord.app_commands.Choice(name="UTC+5", value=5),
        discord.app_commands.Choice(name="UTC+6", value=6),
        discord.app_commands.Choice(name="UTC+7", value=7),
        discord.app_commands.Choice(name="UTC+8", value=8),
        discord.app_commands.Choice(name="UTC+9", value=9),
        discord.app_commands.Choice(name="UTC+10", value=10),
        discord.app_commands.Choice(name="UTC+11", value=11),
        discord.app_commands.Choice(name="UTC+12", value=12),
    ],
    format=[
        discord.app_commands.Choice(name="Short Date", value="d"),
        discord.app_commands.Choice(name="Date and Time", value="f"),
        discord.app_commands.Choice(name="Short Time", value="t"),
        discord.app_commands.Choice(name="Long Date", value="D"),
        discord.app_commands.Choice(name="Day, Date, and Time", value="F"),
        discord.app_commands.Choice(name="Relative (countdown)", value="R"),
        discord.app_commands.Choice(name="Long Time", value="T"),
    ],
)
async def timestamp_builder(
    interaction,
    year: typing.Optional[int] = None,
    month: typing.Optional[int] = None,
    day: typing.Optional[int] = None,
    hour: typing.Optional[int] = None,
    minute: typing.Optional[int] = None,
    second: typing.Optional[int] = 0,
    timezone: typing.Optional[int] = 0,
    format: typing.Optional[str] = None,
    ephemeral: typing.Optional[bool] = True,
):
    current_datetime = datetime.datetime.utcnow()

    if year is None:
        year = current_datetime.year
    if month is None:
        month = current_datetime.month
    if day is None:
        day = current_datetime.day
    if hour is None:
        hour = current_datetime.hour
    if minute is None:
        minute = current_datetime.minute

    adjusted_hour = hour + timezone
    adjusted_hour %= 24

    adjusted_datetime = datetime.datetime(
        year, month, day, adjusted_hour, minute, second
    )

    formatted_date = adjusted_datetime.strftime("%Y-%m-%d")
    formatted_time = adjusted_datetime.strftime("%H:%M:%S")

    formatted_timestamps = [
        f"`<t:{int(adjusted_datetime.timestamp())}:d>` → <t:{int(adjusted_datetime.timestamp())}:d>",
        f"`<t:{int(adjusted_datetime.timestamp())}:f>` → <t:{int(adjusted_datetime.timestamp())}:f>",
        f"`<t:{int(adjusted_datetime.timestamp())}:t>` → <t:{int(adjusted_datetime.timestamp())}:t>",
        f"`<t:{int(adjusted_datetime.timestamp())}:D>` → <t:{int(adjusted_datetime.timestamp())}:D>",
        f"`<t:{int(adjusted_datetime.timestamp())}:F>` → <t:{int(adjusted_datetime.timestamp())}:F>",
        f"`<t:{int(adjusted_datetime.timestamp())}:R>` → <t:{int(adjusted_datetime.timestamp())}:R>",
        f"`<t:{int(adjusted_datetime.timestamp())}:T>` → <t:{int(adjusted_datetime.timestamp())}:T>",
    ]

    if format is None:
        formatted_output = "\n".join(formatted_timestamps)
    else:
        formatted_output = adjusted_datetime.strftime(
            f"`<t:{int(adjusted_datetime.timestamp())}:{format}>` → <t:{int(adjusted_datetime.timestamp())}:{format}>"
        )

    await interaction.response.send_message(
        f"📅 {formatted_date} • 🕒 {formatted_time} • 🌐 UTC\n" f"{formatted_output}",
        ephemeral=ephemeral,
    )


# Event handler for when the bot is ready
@client.event
async def on_ready():
    # Set the bot's status activity
    await client.change_presence(activity=status)
    # Sync the command tree with the guild
    await tree.sync(guild=discord.Object(id=discordID))
    # Print a message to indicate the bot is ready
    print("Bot ready")


# Retrieve the bot token from the environment variable and run the client
token = client.run(os.getenv("BOT_TOKEN"))
client.run(token)
