cmake_minimum_required(VERSION 3.16)
project(e2e C CXX)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_executable(e2e src/main.cpp src/shape.cpp src/util.c src/flags.c)
