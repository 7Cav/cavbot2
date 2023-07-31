FROM python:slim

WORKDIR /app

ADD . /app

RUN pip install -r requirements.txt

CMD ["python3", "-u", "main.py"]