FROM python:3.9

WORKDIR /app

# Copia il codice e installa le dipendenze
COPY . .
RUN pip install --no-cache-dir -r requirements.txt
RUN mkdir -p static
# Esponi la porta per l'API
EXPOSE 8000

# Comando per avviare l'API
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
