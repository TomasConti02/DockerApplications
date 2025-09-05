from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from motor.motor_asyncio import AsyncIOMotorClient
from fastapi.middleware.cors import CORSMiddleware 
from typing import List
import uvicorn

app = FastAPI(title="Simple MongoDB REST API")
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # oppure metti ["http://127.0.0.1:5500"] se usi un webserver
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)
MONGO_URI = "mongodb://localhost:27017"
client = AsyncIOMotorClient(MONGO_URI)
db = client["appdb"]
users_collection = db["users"]

class User(BaseModel):
    userId: int
    name: str
    email: str

@app.get("/users", response_model=List[User])
async def get_users():
    users_cursor = users_collection.find({})
    users = await users_cursor.to_list(length=100)
    return users

@app.post("/users", response_model=User)
async def create_user(user: User):
    existing = await users_collection.find_one({"userId": user.userId})
    if existing:
        raise HTTPException(status_code=400, detail="UserId already exists")
    await users_collection.insert_one(user.dict())
    return user

@app.get("/users/{user_id}", response_model=User)
async def get_user(user_id: int):
    user = await users_collection.find_one({"userId": user_id})
    if not user:
        raise HTTPException(status_code=404, detail="User not found")
    return user

@app.put("/users/{user_id}", response_model=User)
async def update_user(user_id: int, updated_user: User):
    result = await users_collection.update_one(
        {"userId": user_id},
        {"$set": updated_user.dict()}
    )
    if result.matched_count == 0:
        raise HTTPException(status_code=404, detail="User not found")
    return updated_user

@app.delete("/users/{user_id}")
async def delete_user(user_id: int):
    result = await users_collection.delete_one({"userId": user_id})
    if result.deleted_count == 0:
        raise HTTPException(status_code=404, detail="User not found")
    return {"detail": f"User {user_id} deleted successfully"}

if __name__ == "__main__":
    uvicorn.run("app:app", host="0.0.0.0", port=8000, reload=True)
