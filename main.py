import asyncio
import os

import aiohttp
import discord
from discord import app_commands

intents = discord.Intents.all()
intents.message_content = True

client = discord.Client(intents=intents)

tree = app_commands.CommandTree(client)

status = discord.Activity(name="https://7cav.us", type=3)

servers_dict = {
    "A3 TacR": "d7291177",
    "A3 Training 1": "9fd28d83",
    "A3 Training 2": "6b20f439",
    "A3 Training 3": "7b217461",
    "SQ Training": "7e1451cc",
}
api_key = os.getenv("API_KEY")


def can_restart_servers():
    return app_commands.checks.has_any_role("General Staff", "Administrator")


@tree.error
async def on_command_error(
    interaction: discord.Interaction, error: discord.app_commands.AppCommandError
) -> None:
    if isinstance(error, app_commands.errors.MissingAnyRole):
        await interaction.response.send_message(
            f"You must have one of these roles: `{error.missing_roles}` in order to use this command",
            ephemeral=True,
        )
    else:
        sypolt = await client.fetch_user(130158049968128000)
        await sypolt.send(
            f"Someone broke your bot in a new and interesting way ```{error}```"
        )
        await interaction.response.send_message(
            "You managed to break the bot in a way I didn't expect, good job. If the Cav \
had an Army Bug Finder medal I'd give it to you. Anyway the error was forwarded to me, Sypolt.R. \
Why don't you try whatever that was again but with less breaking things this time?",
            ephemeral=True,
        )


@tree.command(
    name="restart_server",
    description="For SGT+ to restart Arma and Squad servers",
    guild=discord.Object(id=109869242148491264),
)
# @can_restart_servers()
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
    await interaction.response.defer()
    server_id = servers_dict[servers.name]
    url = f"https://panel.7cav.us/api/client/servers/{server_id}/power"
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Accept": "application/json",
        "Content-Type": "application/json",
    }
    payload_kill = '{"signal": "kill"}'
    payload_start = '{"signal": "start"}'
    async with aiohttp.ClientSession() as session:
        async with session.post(url, headers=headers, data=payload_kill) as response:
            if response.status == 204:
                await asyncio.sleep(5)
                async with aiohttp.ClientSession() as session:
                    async with session.post(
                        url, headers=headers, data=payload_start
                    ) as response:
                        if response.status == 204:
                            await interaction.followup.send(
                                f"Server {servers.name} restarted successfully!"
                            )
                        else:
                            await interaction.follwup.send(
                                f"Server start failed on {servers.name}."
                            )
            else:
                await interaction.followup.send(
                    f"Failed to stop server {servers.name} {response}."
                )


@client.event
async def on_ready():
    await client.change_presence(activity=status)
    await tree.sync(guild=discord.Object(id=109869242148491264))
    print("Bot ready")


token = client.run(os.getenv("BOT_TOKEN"))
client.run(token)
