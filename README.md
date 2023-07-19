# CavBot2 Readme

This code represents a Discord bot custom built for the 7th Cavalry Gaming Regiment that interacts with the Discord API using the `discord.py` library. The bot provides functionality to restart servers running on Pterodactyl through slash commands.

## Prerequisites

- Python 3.7 or higher
- `discord.py` library
- `aiohttp` library

## Setup

1. Install the required libraries by running the following command:
   `pip install discord.py aiohttp`

2. Obtain a Discord bot token:

- Create a new Discord application and bot on the Discord Developer Portal.
- Copy the bot token and set it as the value of the `BOT_TOKEN` environment variable.

3. Obtain an API key for server control:

- Acquire an API key from the appropriate server control panel.
- Set the API key as the value of the `API_KEY` environment variable.

4. Configure the server dictionary:

- Edit the `servers_dict` variable in the code to include the desired server names and their corresponding server IDs.

5. Customize the bot behavior (optional):

- Modify the `status` variable to set the bot's activity.
- Customize the error handling behavior in the `on_command_error` function.

## Usage

1. Run the script:
   `python main.py`

2. The bot will log in to Discord using the provided bot token and connect to the specified guild.

3. Interact with the bot on Discord:

- Use the `/restart_server` command followed by the desired server name to restart a server.
- Only users with the appropriate roles will be able to use the command successfully.

## Contributing

Feel free to contribute to this code by submitting issues or pull requests on the GitHub repository.

## License

This code is licensed under the [MIT License](https://opensource.org/licenses/MIT).
