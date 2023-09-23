import aiohttp
import asyncio
import logging
import discord
import traceback
from discord import app_commands
import json
import os

class ServerManagement:

    def __init__(self, client, tree, sypolt):
        self.client = client
        self.discord_id = int(os.getenv("DISCORD_ID"))
        with open('./config/config.json') as f:
            self.servers_dict = json.load(f)["servers_dict"]
        self.api_key = os.getenv("API_KEY")
        self.sypolt = sypolt
        self.tree = tree

        @self.tree.command(
            name="restart_server",
            description="For authorized persons to restart Arma and Squad servers",
            guild=discord.Object(id=self.discord_id),
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
        async def restart_server(interaction, servers: app_commands.Choice[int]):
            """Restart a server."""
            await interaction.response.defer()
            username = interaction.user.name
            logging.info(f"{username} initiated the restart_server command.")

            # Get the server ID based on the chosen server name
            server_id = self.servers_dict[servers.name]

            # Prepare the API request URL and headers
            url = f"https://panel.7cav.us/api/client/servers/{server_id}/power"
            headers = {
                "Authorization": f"Bearer {self.api_key}",
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
                                    await sypolt.send(
                                        f"DEBUG: {username} used /restart_server and {servers.name} restarted successfully!")
                                else:
                                    logging.error(f"Server start failed on {servers.name}.")
                                    await interaction.followup.send(f"❌ Server start failed on {servers.name}.")
                                    await sypolt.send(
                                        "DEBUG:{username} used /restart_server and server start failed on {servers.name}.")
                        else:
                            logging.error(f"Failed to stop server {servers.name}.")
                            await interaction.followup.send(f"❌ Failed to stop server {servers.name}.")
                            await sypolt.send(
                                f"DEBUG: {username} used /restart_server and failed to stop server {servers.name}.")
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
        @self.tree.error
        async def on_command_error(
            interaction: discord.Interaction, error: discord.app_commands.AppCommandError
        ) -> None:
            """Handle errors from the command tree."""
            # Forward the error message to the bot owner and respond with a generic error message
            logging.error(f"Sypolt Send! Error: {error}")
            await sypolt.send(
                f"Someone broke your bot in a new and interesting way ```{error}```"

            )
            await interaction.followup.send(
            "You managed to break the bot in a way I didn't expect, good job. If the Cav \
had an Army Bug Finder medal I'd give it to you. Anyway, the error was forwarded to me, Sypolt.R.",
        ephemeral = False,

            )

