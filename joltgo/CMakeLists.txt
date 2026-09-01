cmake_minimum_required(VERSION 3.20)
project(jolt_c LANGUAGES CXX)

# Pull Jolt in as a subproject. This builds only the library (no samples/tests)
# and propagates the exact compile definitions and instruction-set flags that the
# static library was compiled with, which is required for a correct link.
add_subdirectory(${CMAKE_CURRENT_SOURCE_DIR}/../JoltPhysics/Build ${CMAKE_BINARY_DIR}/Jolt)

add_library(jolt_c SHARED wrapper/jolt_c.cpp)
target_include_directories(jolt_c PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/wrapper)
target_link_libraries(jolt_c PRIVATE Jolt)

# Statically link the C++ runtime into the DLL so the Go binary only needs
# jolt_c.dll beside it (no libstdc++-6.dll / libgcc / winpthread dependencies).
if(MINGW)
	target_link_options(jolt_c PRIVATE -static)
endif()
